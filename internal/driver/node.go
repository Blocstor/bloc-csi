package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"

	"github.com/blocstor/bloc-csi/internal/manager"
)

// NodeServer implements the CSI Node service.
type NodeServer struct {
	csi.UnimplementedNodeServer
	nodeName string
	mounter  mount.Interface
	client   *manager.Client
}

// NewNodeServer creates a NodeServer.
func NewNodeServer(nodeName string, managerURL string) *NodeServer {
	return &NodeServer{
		nodeName: nodeName,
		mounter:  mount.New(""),
		client:   manager.NewClient(managerURL),
	}
}

// NodeStageVolume formats (if needed) and mounts the device to the staging path.
func (s *NodeServer) NodeStageVolume(_ context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	stagingPath := req.GetStagingTargetPath()

	// Ensure staging directory exists.
	if err := os.MkdirAll(stagingPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "create staging dir %s: %v", stagingPath, err)
	}

	// Check if already mounted — idempotent.
	notMnt, err := s.mounter.IsLikelyNotMountPoint(stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount point %s: %v", stagingPath, err)
	}
	if !notMnt {
		return &csi.NodeStageVolumeResponse{}, nil
	}

	device, err := s.resolveDevice(req.GetVolumeId(), req.GetPublishContext(), stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve device: %v", err)
	}

	if err := ensureFormatted(device); err != nil {
		return nil, status.Errorf(codes.Internal, "format device %s: %v", device, err)
	}

	if err := s.mounter.Mount(device, stagingPath, "ext4", []string{}); err != nil {
		return nil, status.Errorf(codes.Internal, "mount %s to %s: %v", device, stagingPath, err)
	}

	return &csi.NodeStageVolumeResponse{}, nil
}

// resolveDevice returns the block device path for the volume.
// For NBD volumes it loads the nbd module, connects to the server, and records
// the /dev/nbdN path in a marker file next to the staging directory.
func (s *NodeServer) resolveDevice(volumeID string, pubCtx map[string]string, stagingPath string) (string, error) {
	nbdHost := pubCtx["nbd_host"]
	nbdPortStr := pubCtx["nbd_port"]

	if nbdHost != "" && s.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if res, err := s.client.PublishVolume(ctx, volumeID, s.nodeName); err == nil {
			nbdHost = res.NBDHost
			nbdPortStr = strconv.Itoa(res.NBDPort)
		}
	}

	if nbdHost == "" || nbdPortStr == "" {
		// Plain block device — return as-is.
		dev := pubCtx["device"]
		if dev == "" {
			return "", fmt.Errorf("device not found in publish context")
		}
		return dev, nil
	}

	markerFile := stagingPath + ".bloc-nbd-device"

	// Reuse existing connection if the marker file exists and is active.
	if data, err := os.ReadFile(markerFile); err == nil {
		dev := strings.TrimSpace(string(data))
		if dev != "" {
			var nbdIdx int
			if _, err := fmt.Sscanf(dev, "/dev/nbd%d", &nbdIdx); err == nil {
				sizePath := fmt.Sprintf("/sys/block/nbd%d/size", nbdIdx)
				if sizeData, err := os.ReadFile(sizePath); err == nil {
					if strings.TrimSpace(string(sizeData)) != "0" {
						return dev, nil
					}
				}
			}
		}
	}

	nbdPort, err := strconv.Atoi(nbdPortStr)
	if err != nil {
		return "", fmt.Errorf("invalid nbd_port %q: %w", nbdPortStr, err)
	}

	if err := loadNBDModule(); err != nil {
		return "", fmt.Errorf("load nbd module: %w", err)
	}

	dev, err := connectNBD(nbdHost, nbdPort)
	if err != nil {
		return "", fmt.Errorf("connect nbd %s:%d: %w", nbdHost, nbdPort, err)
	}

	if err := os.WriteFile(markerFile, []byte(dev), 0600); err != nil {
		// Non-fatal — worst case we leak an nbd device on next stage.
		_ = exec.Command("nbd-client", "-d", dev).Run()
		return "", fmt.Errorf("write nbd marker %s: %w", markerFile, err)
	}

	return dev, nil
}

// loadNBDModule verifies nbd.ko is loaded; in Talos the module is loaded via
// machine config so this only checks and errors if the module is absent.
func loadNBDModule() error {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return fmt.Errorf("read /proc/modules: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "nbd ") {
			return nil
		}
	}
	return fmt.Errorf("nbd module not loaded; add nbd to machine.kernel.modules in Talos config")
}

// connectNBD finds a free /dev/nbdN, connects it to the NBD server, and returns
// the device path.
func connectNBD(host string, port int) (string, error) {
	// Find free /dev/nbdN (size == 0 means not connected).
	var free string
	for i := 0; i < 64; i++ {
		sizePath := fmt.Sprintf("/sys/block/nbd%d/size", i)
		data, err := os.ReadFile(sizePath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == "0" {
			free = fmt.Sprintf("/dev/nbd%d", i)
			break
		}
	}
	if free == "" {
		return "", fmt.Errorf("no free /dev/nbd* devices found")
	}

	// Use full path; nbd-client 3.24+ still accepts old-style host port device.
	cmd := exec.Command("/usr/sbin/nbd-client", host, strconv.Itoa(port), free)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("nbd-client %s %d %s: %v: %s", host, port, free, err, string(out))
	}

	// Verify the device is connected (size becomes non-zero after connect).
	idx := strings.TrimPrefix(free, "/dev/nbd")
	for i := 0; i < 20; i++ {
		data, _ := os.ReadFile(fmt.Sprintf("/sys/block/nbd%s/size", idx))
		if strings.TrimSpace(string(data)) != "0" {
			return free, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("nbd device %s connected but size remains 0 after 2s", free)
}

// ensureFormatted runs mkfs.ext4 on the device if it has no filesystem.
func ensureFormatted(device string) error {
	out, err := exec.Command("blkid", "-o", "value", "-s", "TYPE", device).Output()
	if err == nil && len(out) > 0 {
		return nil
	}
	cmd := exec.Command("mkfs.ext4", "-F", device)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s: %v: %s", device, err, string(out))
	}
	return nil
}

// NodePublishVolume bind-mounts from staging path to target path.
func (s *NodeServer) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	targetPath := req.GetTargetPath()
	stagingPath := req.GetStagingTargetPath()

	if err := os.MkdirAll(targetPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "create target dir %s: %v", targetPath, err)
	}

	notMnt, err := s.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount point %s: %v", targetPath, err)
	}
	if !notMnt {
		return &csi.NodePublishVolumeResponse{}, nil
	}

	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}
	if err := s.mounter.Mount(stagingPath, targetPath, "", mountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount %s to %s: %v", stagingPath, targetPath, err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmounts the target path.
func (s *NodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}

	if err := mount.CleanupMountPoint(req.GetTargetPath(), s.mounter, false); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount %s: %v", req.GetTargetPath(), err)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeUnstageVolume unmounts the staging path and disconnects any NBD device.
func (s *NodeServer) NodeUnstageVolume(_ context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	stagingPath := req.GetStagingTargetPath()

	if err := mount.CleanupMountPoint(stagingPath, s.mounter, false); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount staging %s: %v", stagingPath, err)
	}

	// Disconnect NBD device if one was recorded.
	markerFile := stagingPath + ".bloc-nbd-device"
	if data, err := os.ReadFile(markerFile); err == nil {
		dev := strings.TrimSpace(string(data))
		if dev != "" {
			exec.Command("nbd-client", "-d", dev).Run() //nolint:errcheck
		}
		os.Remove(markerFile) //nolint:errcheck
	}

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodeGetCapabilities returns node capabilities.
func (s *NodeServer) NodeGetCapabilities(_ context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

// NodeGetInfo returns information about the node.
func (s *NodeServer) NodeGetInfo(_ context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	nodeID := s.nodeName
	if nodeID == "" {
		nodeID = os.Getenv("NODE_NAME")
	}
	return &csi.NodeGetInfoResponse{
		NodeId: nodeID,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				"topology.bloc.csi.blocstor/node": nodeID,
			},
		},
	}, nil
}


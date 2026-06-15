package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"
)

// NodeServer implements the CSI Node service.
type NodeServer struct {
	csi.UnimplementedNodeServer
	nodeName string
	mounter  mount.Interface
}

// NewNodeServer creates a NodeServer.
func NewNodeServer(nodeName string) *NodeServer {
	return &NodeServer{
		nodeName: nodeName,
		mounter:  mount.New(""),
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

	device, ok := req.GetPublishContext()["device"]
	if !ok || device == "" {
		return nil, status.Error(codes.InvalidArgument, "device not found in publish context")
	}
	stagingPath := req.GetStagingTargetPath()

	// Ensure staging directory exists.
	if err := os.MkdirAll(stagingPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "create staging dir %s: %v", stagingPath, err)
	}

	// Check if already mounted.
	notMnt, err := s.mounter.IsLikelyNotMountPoint(stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount point %s: %v", stagingPath, err)
	}
	if !notMnt {
		// Already mounted — idempotent.
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Check if device already has a filesystem.
	if err := ensureFormatted(device); err != nil {
		return nil, status.Errorf(codes.Internal, "format device %s: %v", device, err)
	}

	// Mount device to staging path.
	if err := s.mounter.Mount(device, stagingPath, "ext4", []string{}); err != nil {
		return nil, status.Errorf(codes.Internal, "mount %s to %s: %v", device, stagingPath, err)
	}

	return &csi.NodeStageVolumeResponse{}, nil
}

// ensureFormatted runs mkfs.ext4 on the device if it has no filesystem.
func ensureFormatted(device string) error {
	// blkid exits non-zero when no filesystem is found.
	out, err := exec.Command("blkid", "-o", "value", "-s", "TYPE", device).Output()
	if err == nil && len(out) > 0 {
		// Already has a filesystem.
		return nil
	}
	// Format with ext4.
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

	// Ensure target directory exists.
	if err := os.MkdirAll(targetPath, 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "create target dir %s: %v", targetPath, err)
	}

	// Check if already mounted.
	notMnt, err := s.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount point %s: %v", targetPath, err)
	}
	if !notMnt {
		// Already bind-mounted — idempotent.
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Bind mount from staging to target.
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

// NodeUnstageVolume unmounts the staging path.
func (s *NodeServer) NodeUnstageVolume(_ context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	if err := mount.CleanupMountPoint(req.GetStagingTargetPath(), s.mounter, false); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount staging %s: %v", req.GetStagingTargetPath(), err)
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

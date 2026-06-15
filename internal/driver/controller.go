package driver

import (
	"context"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/blocstor/bloc-csi/internal/manager"
)

// ControllerServer implements the CSI Controller service.
type ControllerServer struct {
	csi.UnimplementedControllerServer
	mgr          *manager.Client
	defaultNodes []string
}

// NewControllerServer creates a ControllerServer.
func NewControllerServer(mgr *manager.Client, defaultNodes []string) *ControllerServer {
	return &ControllerServer{mgr: mgr, defaultNodes: defaultNodes}
}

// CreateVolume provisions a new DRBD volume via bloc-manager.
func (s *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}

	// Determine size in MB (minimum 1 MB).
	sizeMB := int64(1)
	if cr := req.GetCapacityRange(); cr != nil {
		if cr.GetRequiredBytes() > 0 {
			sizeMB = cr.GetRequiredBytes() / (1024 * 1024)
			if sizeMB < 1 {
				sizeMB = 1
			}
		}
	}

	// Determine target nodes from topology requirements or defaults.
	nodes := s.defaultNodes
	if ar := req.GetAccessibilityRequirements(); ar != nil {
		nodeSet := make(map[string]struct{})
		for _, topo := range ar.GetPreferred() {
			if n, ok := topo.GetSegments()["topology.bloc.csi.blocstor/node"]; ok {
				nodeSet[n] = struct{}{}
			}
		}
		for _, topo := range ar.GetRequisite() {
			if n, ok := topo.GetSegments()["topology.bloc.csi.blocstor/node"]; ok {
				nodeSet[n] = struct{}{}
			}
		}
		if len(nodeSet) > 0 {
			nodes = make([]string, 0, len(nodeSet))
			for n := range nodeSet {
				nodes = append(nodes, n)
			}
		}
	}

	vol, err := s.mgr.CreateVolume(ctx, req.GetName(), int(sizeMB), nodes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create volume: %v", err)
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      vol.ID,
			CapacityBytes: int64(vol.SizeMB) * 1024 * 1024,
		},
	}, nil
}

// DeleteVolume deletes a DRBD volume. Returns success on 404 (idempotent).
func (s *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	err := s.mgr.DeleteVolume(ctx, req.GetVolumeId())
	if err != nil {
		if manager.IsNotFound(err) {
			return &csi.DeleteVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "delete volume: %v", err)
	}

	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerPublishVolume attaches a volume to a node.
func (s *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID is required")
	}

	result, err := s.mgr.PublishVolume(ctx, req.GetVolumeId(), req.GetNodeId())
	if err != nil {
		if manager.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "volume %s not found", req.GetVolumeId())
		}
		return nil, status.Errorf(codes.Internal, "publish volume: %v", err)
	}

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{
			"device": result.Device,
		},
	}, nil
}

// ControllerUnpublishVolume detaches a volume from a node.
func (s *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	err := s.mgr.UnpublishVolume(ctx, req.GetVolumeId())
	if err != nil {
		if manager.IsNotFound(err) {
			return &csi.ControllerUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "unpublish volume: %v", err)
	}

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ControllerExpandVolume resizes a DRBD volume.
func (s *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if req.GetCapacityRange() == nil {
		return nil, status.Error(codes.InvalidArgument, "capacity range is required")
	}

	newSizeMB := int(req.GetCapacityRange().GetRequiredBytes() / (1024 * 1024))
	if newSizeMB < 1 {
		newSizeMB = 1
	}

	if err := s.mgr.ResizeVolume(ctx, req.GetVolumeId(), newSizeMB); err != nil {
		if manager.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "volume %s not found", req.GetVolumeId())
		}
		return nil, status.Errorf(codes.Internal, "resize volume: %v", err)
	}

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         req.GetCapacityRange().GetRequiredBytes(),
		NodeExpansionRequired: false,
	}, nil
}

// ControllerGetCapabilities returns controller capabilities.
func (s *ControllerServer) ControllerGetCapabilities(_ context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	caps := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
	}

	var csiCaps []*csi.ControllerServiceCapability
	for _, c := range caps {
		csiCaps = append(csiCaps, &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: c,
				},
			},
		})
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: csiCaps}, nil
}

// ValidateVolumeCapabilities checks if volume capabilities are supported.
func (s *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	_, err := s.mgr.GetVolume(ctx, req.GetVolumeId())
	if err != nil {
		if manager.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "volume %s not found", req.GetVolumeId())
		}
		return nil, status.Errorf(codes.Internal, "get volume: %v", err)
	}

	// Only SINGLE_NODE_WRITER is supported (DRBD primary access mode).
	for _, cap := range req.GetVolumeCapabilities() {
		if cap.GetAccessMode() != nil {
			mode := cap.GetAccessMode().GetMode()
			if mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER &&
				mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY {
				return &csi.ValidateVolumeCapabilitiesResponse{
					Message: "only SINGLE_NODE_WRITER and SINGLE_NODE_READER_ONLY access modes are supported",
				}, nil
			}
		}
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// parseNodes splits a comma-separated node list.
func parseNodes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

package driver

import (
	"context"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const PluginName = "bloc.csi.blocstor"

// Version is set at build time via ldflags.
var Version = "dev"

// IdentityServer implements the CSI Identity service.
type IdentityServer struct {
	csi.UnimplementedIdentityServer
}

// GetPluginInfo returns driver name and version.
func (s *IdentityServer) GetPluginInfo(_ context.Context, req *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	return &csi.GetPluginInfoResponse{
		Name:          PluginName,
		VendorVersion: Version,
	}, nil
}

// GetPluginCapabilities returns capabilities supported by this driver.
func (s *IdentityServer) GetPluginCapabilities(_ context.Context, req *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS,
					},
				},
			},
		},
	}, nil
}

// Probe returns the health of the driver.
func (s *IdentityServer) Probe(_ context.Context, req *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	return &csi.ProbeResponse{
		Ready: wrapperspb.Bool(true),
	}, nil
}

# bloc-csi

Kubernetes CSI driver for DRBD block storage, part of the [Blocstor](https://github.com/blocstor) stack.

## Architecture

- **Controller** (Deployment): handles volume lifecycle by calling bloc-manager's REST API
  - CreateVolume / DeleteVolume
  - ControllerPublishVolume / ControllerUnpublishVolume
  - ControllerExpandVolume
- **Node** (DaemonSet): runs on every node, handles local staging and bind-mounting
  - NodeStageVolume: formats (ext4, if needed) and mounts the DRBD device to a staging path
  - NodePublishVolume: bind-mounts from staging path to pod target path
  - NodeUnpublishVolume / NodeUnstageVolume: unmounts in reverse order

The CSI driver name is `bloc.csi.blocstor`.

## Requirements

- bloc-manager running and reachable from the cluster
- bloc-agent running on storage nodes
- DRBD 9.x kernel module on storage nodes
- Kubernetes 1.26+

## Install

### Via OCI Helm chart

```sh
helm install bloc-csi oci://ghcr.io/blocstor/helm/bloc-csi \
  --namespace bloc-system --create-namespace \
  --set manager.url=http://<bloc-manager-ip>:9090
```

### From source

```sh
git clone https://github.com/blocstor/bloc-csi
helm install bloc-csi ./helm/bloc-csi \
  --namespace bloc-system --create-namespace \
  --set manager.url=http://<bloc-manager-ip>:9090
```

## Configuration

Key `values.yaml` fields:

| Field | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/blocstor/bloc-csi` | Driver image |
| `image.tag` | `latest` | Image tag |
| `manager.url` | `http://bloc-manager.bloc-system.svc.cluster.local:9090` | bloc-manager endpoint |
| `storageClass.create` | `true` | Whether to create the default StorageClass |
| `storageClass.name` | `bloc-drbd` | StorageClass name |
| `storageClass.parameters.nodes` | `cs1,cs2` | Comma-separated storage node list |

## Building

```sh
# Build binary
go build -ldflags="-X github.com/blocstor/bloc-csi/internal/driver.Version=$(git describe --tags)" \
  -o bloc-csi ./cmd/driver

# Build container image
docker build -t ghcr.io/blocstor/bloc-csi:dev .
```

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

## License

Apache 2.0 — see [LICENSE](LICENSE).

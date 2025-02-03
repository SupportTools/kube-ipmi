# kube-ipmi

**kube-ipmi** is a containerized tool that collects hardware information (via IPMI and/or `dmidecode`) and annotates Kubernetes Nodes with details such as manufacturer, model, serial number, IP address, MAC address, and (for Dell hardware) the Dell Service Tag and Express Service Code.

This lets you easily reference hardware metadata in the Kubernetes API—useful for inventory, auditing, or scheduling decisions based on physical server attributes.

---

## Features

1. **IPMI Data**  
   - Gathers FRU information (`ipmitool fru print`) for manufacturer, model, and serial.  
   - Gathers LAN information (`ipmitool lan print 1`) for IP address and MAC.  

2. **Dell Enhancements**  
   - Attempts to identify Dell servers and extract the short “Dell Service Tag” and compute the Express Service Code (base-36 → decimal).  

3. **DMI Override (optional)**  
   - Falls back to local `dmidecode --type system` if IPMI data is missing or incomplete.  
   - This can provide the official Dell Inc. manufacturer name, short service tag, etc., which may differ from IPMI’s FRU data.

4. **Kubernetes Node Annotations**  
   - Annotates the Node where the Pod runs under `ipmi.support.tools/*` keys, such as:
    ```yaml
    ipmi.support.tools/dell-express-code: 36953844517
    ipmi.support.tools/dell-service-tag: GZ5D4X1
    ipmi.support.tools/ip-address: 172.28.10.99
    ipmi.support.tools/mac-address: 78:45:c4:f5:16:6d
    ipmi.support.tools/manufacturer: Dell Inc.
    ipmi.support.tools/model: PowerEdge R720xd
    ipmi.support.tools/serial-number: GZ5D4X1
    ```

---

## How It Works

1. **Init Container or DaemonSet Pod**  
   - The container runs on each node (via a Kubernetes DaemonSet or as an init container).  
   - It needs privileged access (and possibly a host mount of `/dev/ipmi0`) to run `ipmitool`.  

2. **IPMI + DMI Collection**  
   - The Go-based utility calls:
     - `ipmitool fru print` to parse manufacturer, model, and serial.
     - `ipmitool lan print 1` to parse IP address and MAC address (on channel 1).
     - `dmidecode --type system` for local overrides (if desired).

3. **Node Annotation**  
   - The utility uses an in-cluster Kubernetes client (via `rest.InClusterConfig()`) and the `NODE_NAME` environment variable to identify which Node object to update.  
   - Annotates the Node with the discovered hardware information.

---

## Deployment Example

Below is a **DaemonSet** example that:

- Runs one Pod per node in the `kube-ipmi` namespace.
- Has an **init container** (`kube-ipmi`) that gathers hardware info and updates the node’s annotations.
- Uses a simple “sleep forever” main container so the Pod remains running.
- Is privileged and mounts `/dev/ipmi0`, allowing `ipmitool` access to the system BMC.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kube-ipmi
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kube-ipmi-sa
  namespace: kube-ipmi
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kube-ipmi-role
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kube-ipmi-rolebinding
subjects:
  - kind: ServiceAccount
    name: kube-ipmi-sa
    namespace: kube-ipmi
roleRef:
  kind: ClusterRole
  name: kube-ipmi-role
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kube-ipmi
  namespace: kube-ipmi
spec:
  selector:
    matchLabels:
      app: kube-ipmi
  template:
    metadata:
      labels:
        app: kube-ipmi
    spec:
      serviceAccountName: kube-ipmi-sa
      nodeSelector:
        kubernetes.io/os: linux
        kubernetes.io/arch: amd64
      volumes:
        - name: dev-ipmi
          hostPath:
            path: /dev/ipmi0
      initContainers:
        - name: kube-ipmi
          # This image must include ipmitool + dmidecode
          image: supporttools/kube-ipmi:latest
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          securityContext:
            privileged: true
          volumeMounts:
            - name: dev-ipmi
              mountPath: /dev/ipmi0
      containers:
        - name: sleep-forever
          image: busybox:latest
          command: ["sh", "-c", "sleep", "infinity"]
```

### RBAC

- The Pod uses the `kube-ipmi-sa` **ServiceAccount**.  
- The `ClusterRole` + `ClusterRoleBinding` allow `get`, `update`, and `patch` on the Node objects.

### Privileged + `/dev/ipmi0`

- For `ipmitool` to communicate with the BMC locally, the init container is privileged and has a host mount of `/dev/ipmi0`.
- Adjust if your IPMI device path differs (`/dev/ipmi1`, etc.) or if you need an `ipmi-si` kernel module loaded.

---

## Building the Image

Below is an example multi-stage Dockerfile that compiles the Go program, includes `ipmitool` and `dmidecode` in the final Alpine image:

```dockerfile
# 1) Builder
FROM golang:1.23.5-alpine AS builder
RUN apk update && apk add --no-cache git bash
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION
ARG GIT_COMMIT
ARG BUILD_DATE
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -ldflags "-s -w" \
    -o /kube-ipmi

# 2) Final
FROM alpine:3.18
RUN apk add --no-cache ipmitool dmidecode
COPY --from=builder /kube-ipmi /kube-ipmi
ENTRYPOINT ["/kube-ipmi"]
```

Build and push it to your registry:

```bash
docker build -t your-registry/kube-ipmi:latest .
docker push your-registry/kube-ipmi:latest
```

---

## Usage

Once deployed as a DaemonSet, each node’s Pod will, upon startup:

1. Run the init container:
   - Collect IPMI FRU/LAN data (via `ipmitool`).
   - Optionally run `dmidecode` for local overrides.
   - Annotate the node object with metadata under `ipmi.support.tools/...`.
2. Transition to the main container, which sleeps, keeping the Pod alive.

You can see your Node annotations:

```bash
kubectl get nodes -o yaml | grep -A10 "ipmi.support.tools/"
```

Example final annotations:

```yaml
ipmi.support.tools/dell-express-code: 36953844517
ipmi.support.tools/dell-service-tag: GZ5D4X1
ipmi.support.tools/ip-address: 172.28.10.99
ipmi.support.tools/mac-address: 78:45:c4:f5:16:6d
ipmi.support.tools/manufacturer: Dell Inc.
ipmi.support.tools/model: PowerEdge R720xd
ipmi.support.tools/serial-number: GZ5D4X1
```

---

## License and Contributing

- License: [Apache](LICENSE)
- Contributions welcome! Please open issues or pull requests on the repository.  

---

## Troubleshooting

- **`ipmitool` returns exit code 1** but still prints valid data. We log a warning and parse the output anyway.
- **No `/dev/ipmi0`** on the host? Load the `ipmi_si` kernel module or check your hardware/BMC configuration.
- **`dmidecode: executable file not found`** means you need to ensure `dmidecode` is installed in the final container image (as shown in the Dockerfile).
- **`NODE_NAME` not set** or missing RBAC for updating Nodes leads to annotation failures. Check your environment variables and Role/RoleBinding.

---
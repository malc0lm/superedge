package manifest

const KinsServerTemplate = `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  labels:
    {{ .KinsResourceLabelKey }}: "yes"
    {{ .UnitName }}: {{ .NodeUnitSuperedge }}
  name: {{ .KinsServerName }}
  namespace: {{ .KinsNamespace }}
spec:
  selector:
    matchLabels:
      site.superedge.io/kins-role: server
  template: # create pods using pod definition in this template
    metadata:
      labels:
        site.superedge.io/kins-role: server
      name: k3s-server
    spec:
      tolerations:
      - key: "{{ .KinsTaintKey }}"
        operator: "Exists"
        effect: "NoSchedule"
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: {{ .KinsRoleLabelKey }}
                operator: In
                values:
                - {{ .KinsRoleLabelServer }}
              - key: {{ .UnitName }}
                operator: In
                values:
                - {{ .NodeUnitSuperedge }}
      initContainers:
      - name: mkcgroup
        image: {{ .K3SServerImage }}
        imagePullPolicy: IfNotPresent
        command:
          - /bin/sh
          - -c
          - |
            for d in $(ls /sys/fs/cgroup)
            do
              mkdir -p /sys/fs/cgroup/$d/edgek3s
            done
        volumeMounts:
          - name: host-sys
            mountPath: /sys
      containers:
      - name: server
        image: {{ .K3SServerImage }}
        env:
        - name: K3S_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ .KinsSecretName }}
              key: k3stoken
              optional: false
        securityContext:
          privileged: true
        command: ["/k3s"]
        args: 
        - server
        - --container-runtime-endpoint=/run/kins.sock
        - --flannel-backend=none
        - --disable-kube-proxy
        - --disable-cloud-controller
        - --cluster-cidr=169.254.0.0/16
        - --service-cidr={{ .ServiceCIDR }}               
        - --service-node-port-range={{ .KinsNodePortRange }}
        - --cluster-dns={{ .KinsCorednsIP }}
        - --kube-apiserver-arg=--token-auth-file=/etc/edge/known_tokens.csv
        - --kubelet-arg=--cgroup-root=/edgek3s
        - --kubelet-arg=--root-dir=/data/edge/k3s
        ports:
        - containerPort: 6443
        volumeMounts:
        - name: host-run
          mountPath: /run
          mountPropagation: "Bidirectional"
        - name: host-dev
          mountPath: /dev
        - name: host-sys
          mountPath: /sys
          mountPropagation: "Bidirectional"
        - name: lib-modules
          mountPath: /lib/modules
          readOnly: true
        - name: host-containerd
          mountPath: /var/lib/containerd
        - name: host-docker
          mountPath: /var/lib/docker
        - name: host-kubelet-log
          mountPath: /data/edge/log/pods
        - name: k3sroot
          mountPath: /data/edge/k3s
          mountPropagation: "Bidirectional"
        - name: rancher-root
          mountPath: /var/lib/rancher
        - mountPath: /etc/edge/
          name: token
          readOnly: true
      volumes:
        - hostPath:
            path: /run
          name: host-run
        - hostPath:
            path: /lib/modules
          name: lib-modules
        - name: host-dev
          hostPath:
            path: /dev
        - name: host-sys
          hostPath:
            path: /sys
        - name: host-containerd
          hostPath:
            path: /var/lib/containerd
        - name: host-docker
          hostPath:
            path: /var/lib/docker
        - name: host-kubelet-log
          hostPath:
            path: /data/edge/log/pods
        - name: k3sroot
          hostPath:
            path: /data/edge/k3s
            type: DirectoryOrCreate
        - hostPath:
            path: /var/lib/rancher
          name: rancher-root
        - secret:
            defaultMode: 420
            secretName: {{ .KinsSecretName }}
            items:
              - key: known_tokens.csv
                path: known_tokens.csv
          name: token
`

package unitcluster

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/rand"

	"k8s.io/apimachinery/pkg/util/intstr"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
	clientset "k8s.io/client-go/kubernetes"
	applisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	sitev1alpha2 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/site-manager/constant"
	"github.com/superedge/superedge/pkg/site-manager/controller/unitcluster/manifest"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	crdv1listers "github.com/superedge/superedge/pkg/site-manager/generated/listers/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/site-manager/utils"

	kubectl "github.com/superedge/superedge/pkg/util/kubeclient"
)

type KinsController struct {
	kubeClient     clientset.Interface
	crdClient      *crdClientset.Clientset
	nodeLister     corelisters.NodeLister
	dsLister       applisters.DaemonSetLister
	nodeUnitLister crdv1listers.NodeUnitLister
}

func NewKinsController(
	kubeClient clientset.Interface,
	crdClient *crdClientset.Clientset,
	nodeLister corelisters.NodeLister,
	dsLister applisters.DaemonSetLister,
	nodeUnitLister crdv1listers.NodeUnitLister,
) *KinsController {
	return &KinsController{
		kubeClient,
		crdClient,
		nodeLister,
		dsLister,
		nodeUnitLister,
	}
}

func (kc *KinsController) ReconcileUnitCluster(nu *sitev1alpha2.NodeUnit) error {
	// check if need uninstall unit cluster
	alevel := nu.Spec.AutonomyLevel
	switch alevel {
	case sitev1alpha2.AutonomyLevelL3:
		// L3 should uninstall unitcluster
		return kc.recoverNodeUnit(nu)
	case sitev1alpha2.AutonomyLevelL4:
		// need install single master unitcluster and the storage backend is sqlite
		// nodeunit setnode taints
		return kc.installUnitCluster(nu)
	// update nodeunit

	case sitev1alpha2.AutonomyLevelL5:
		klog.Warningf("Unsupport L5 currently!")
		return nil

	default:
		klog.Warningf("Unsupport AutonomyLevel!")
	}

	return nil
}

func (kc *KinsController) installUnitCluster(nu *sitev1alpha2.NodeUnit) error {
	startTime := time.Now()
	klog.V(4).InfoS("Started install nodeunit cluster", "nodeunit", nu.Name, "startTime", startTime)
	defer func() {
		klog.V(4).InfoS("Finished install nodeunit cluster", "nodeunit", nu.Name, "duration", time.Since(startTime))
	}()

	// ensure kins-system namespace
	if _, err := kc.kubeClient.CoreV1().Namespaces().Get(context.TODO(), DefaultKinsNamespace, metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			if _, err := kc.kubeClient.CoreV1().Namespaces().Create(
				context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DefaultKinsNamespace}},
				metav1.CreateOptions{},
			); err != nil {
				klog.ErrorS(err, "create namespace error", "name", DefaultKinsNamespace)
				return err
			}
		} else {
			klog.ErrorS(err, "get namespace error", "name", DefaultKinsNamespace)
			return err
		}
	}

	// label server
	// TOOD: find a property node as server
	masterFound := false
	_, nodeMap, getNodeErr := utils.GetNodesByUnit(kc.nodeLister, nu)
	if getNodeErr != nil {
		return getNodeErr
	}
	for _, node := range nodeMap {
		if node.Labels[KinsRoleLabelKey] == KinsRoleLabelServer {
			masterFound = true
		}
	}
	// if node unit has not master node label
	if !masterFound {
		var masterNodeName string
		if len(nu.Status.ReadyNodes) > 0 {
			masterNodeName = nu.Status.ReadyNodes[0]
		} else {
			klog.Errorf("Node unit=%s has not ready node", nu.Name)
			return fmt.Errorf("No Ready Node")
		}

		nac := applycorev1.Node(masterNodeName).WithLabels(
			map[string]string{
				KinsRoleLabelKey: KinsRoleLabelServer,
			},
		)
		if _, err := kc.kubeClient.CoreV1().Nodes().Apply(context.TODO(), nac, metav1.ApplyOptions{FieldManager: "application/apply-patch"}); err != nil {
			klog.ErrorS(err, "Update node server label error,node unit %s", nu.Name)
			return err
		}
	}

	var existUnitCluster int32
	// caculate service cidr nodeport range and coredns IP
	if nuList, err := kc.nodeUnitLister.List(labels.Everything()); err != nil {
		for _, nu := range nuList {
			if nu.Spec.AutonomyLevel == sitev1alpha2.AutonomyLevelL4 || nu.Spec.AutonomyLevel == sitev1alpha2.AutonomyLevelL5 {
				existUnitCluster += 1
			}
		}
	}
	uclusterServiceCIDR, uclusterDNSIP := caculateKinsServiceCIDRAndCoreDNSIP(nu, existUnitCluster)

	// create criw ds
	criwOption := map[string]interface{}{
		"KinsResourceLabelKey": KinsResourceLabelKey,
		"CRIWName":             buildKinsCRIDaemonSetName(nu.Name),
		"KinsNamespace":        DefaultKinsNamespace,
		"UnitName":             nu.Name,
		"NodeUnitSuperedge":    constant.NodeUnitSuperedge,
		"KinsTaintKey":         KinsResourceNameSuffix,
		"KinsCRIWImage":        getCRIWImage(nu),
	}
	if err := kubectl.CreateResourceWithFile(kc.kubeClient, manifest.CRIWTemplate, criwOption); err != nil {
		klog.ErrorS(err, "create criw daemonset error")
		return err
	}

	// create kins server
	serverOption := map[string]interface{}{
		"KinsResourceLabelKey": KinsResourceLabelKey,
		"KinsServerName":       buildKinsServerDaemonSetName(nu.Name),
		"KinsNamespace":        DefaultKinsNamespace,
		"KinsTaintKey":         KinsResourceNameSuffix,
		"KinsRoleLabelKey":     KinsRoleLabelKey,
		"KinsRoleLabelServer":  KinsRoleLabelServer,
		"UnitName":             nu.Name,
		"NodeUnitSuperedge":    constant.NodeUnitSuperedge,
		"K3SServerImage":       getK3SImage(nu),
		"KinsSecretName":       buildKinsSecretName(nu.Name),
		"ServiceCIDR":          uclusterServiceCIDR,
		"KinsNodePortRange":    caculateKinsNodePortRange(nu, int(existUnitCluster)),
		"KinsCorednsIP":        uclusterDNSIP,
	}
	if err := kubectl.CreateResourceWithFile(kc.kubeClient, manifest.KinsServerTemplate, serverOption); err != nil {
		klog.ErrorS(err, "create kins server error")
		return err
	}
	var svc *corev1.Service
	var err error
	if svc, err = kc.kubeClient.CoreV1().Services(DefaultKinsNamespace).Get(context.TODO(), buildKinsServiceName(nu.Name), metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			// create kins server service
			svc, err = kc.kubeClient.CoreV1().Services(DefaultKinsNamespace).Create(
				context.TODO(),
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name: buildKinsServiceName(nu.Name),
						Labels: map[string]string{
							KinsResourceLabelKey: "yes",
							nu.Name:              constant.NodeUnitSuperedge,
						},
						Namespace: DefaultKinsNamespace,
					},
					Spec: corev1.ServiceSpec{
						Type: corev1.ServiceTypeClusterIP,
						Selector: map[string]string{
							KinsRoleLabelKey: KinsRoleLabelServer,
						},
						Ports: []corev1.ServicePort{
							{
								Name:     "https",
								Protocol: corev1.ProtocolTCP,
								Port:     443,
								TargetPort: intstr.IntOrString{
									Type:   intstr.Int,
									IntVal: 6443,
								},
							},
						},
					},
				},
				metav1.CreateOptions{},
			)
			if err != nil {
				klog.ErrorS(err, "create kins service error")
				return err
			}
		} else {
			klog.ErrorS(err, "get kins service error")
			return err
		}
	}

	// create kins agent
	agentOption := map[string]interface{}{
		"KinsResourceLabelKey": KinsResourceLabelKey,
		"KinsAgentName":        buildKinsAgentDaemonSetName(nu.Name),
		"KinsNamespace":        DefaultKinsNamespace,
		"KinsTaintKey":         KinsResourceNameSuffix,
		"KinsRoleLabelKey":     KinsRoleLabelKey,
		"KinsRoleLabelServer":  KinsRoleLabelServer,
		"UnitName":             nu.Name,
		"NodeUnitSuperedge":    constant.NodeUnitSuperedge,
		"K3SAgentImage":        getK3SImage(nu),
		"KinsSecretName":       buildKinsSecretName(nu.Name),
		"KinsServerEndpoint":   buildKinsServerEndpoint(svc.Spec.ClusterIP, int(svc.Spec.Ports[0].Port)),
	}
	if err := kubectl.CreateResourceWithFile(kc.kubeClient, manifest.KinsAgentTemplate, agentOption); err != nil {
		klog.ErrorS(err, "create kins agent error")
		return err
	}

	knowToken := generateKinsSecretKnownToken()
	knowTokenBase64 := base64.URLEncoding.EncodeToString([]byte(knowToken))
	// get or create secret
	if secret, err := kc.kubeClient.CoreV1().Secrets(DefaultKinsNamespace).Get(context.TODO(), buildKinsSecretName(nu.Name), metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			// K3STOKEN is same with K3SJOINTOKEN currently
			k3stoken := generateKinsSecretK3SToken(nu.Name)
			secretOption := map[string]interface{}{
				"KinsResourceLabelKey": KinsResourceLabelKey,
				"KinsSecretName":       buildKinsSecretName(nu.Name),
				"UnitName":             nu.Name,
				"NodeUnitSuperedge":    constant.NodeUnitSuperedge,
				"KinsNamespace":        DefaultKinsNamespace,
				"K3SToken":             k3stoken,
				"K3SJoinToken":         k3stoken,
				"KnowToken":            knowTokenBase64,
			}
			if err := kubectl.CreateResourceWithFile(kc.kubeClient, manifest.KinsSecretTemplate, secretOption); err != nil {
				klog.ErrorS(err, "create kins secret error")
				return err
			}
		} else {
			klog.ErrorS(err, "get kins secret error")
			return err
		}
	} else {
		knowToken = secret.StringData["known_tokens.csv"]
	}
	// get or create configmap
	if _, err := kc.kubeClient.CoreV1().ConfigMaps(DefaultKinsNamespace).Get(context.TODO(), buildKinsConfigMapName(nu.Name), metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			configmapOption := map[string]interface{}{
				"KinsResourceLabelKey": KinsResourceLabelKey,
				"KinsConfigMapName":    buildKinsConfigMapName(nu.Name),
				"UnitName":             nu.Name,
				"NodeUnitSuperedge":    constant.NodeUnitSuperedge,
				"KinsNamespace":        DefaultKinsNamespace,
				"K3SEndpoint":          buildKinsServerEndpoint(svc.Spec.ClusterIP, int(svc.Spec.Ports[0].Port)),
				"KnowToken":            strings.Split(knowToken, ",")[0],
			}
			if err := kubectl.CreateResourceWithFile(kc.kubeClient, manifest.KinsConfigMapTemplate, configmapOption); err != nil {
				klog.ErrorS(err, "create kins configmap error")
				return err
			}
		} else {
			klog.ErrorS(err, "get kins configmap error")
			return err
		}
	}

	// set node taint
	hasTaint := false
	for _, t := range nu.Spec.SetNode.Taints {
		if t.Key == KinsResourceNameSuffix && t.Effect == corev1.TaintEffectNoSchedule {
			hasTaint = true
		}
	}

	if !hasTaint || nu.Spec.UnitCredentialConfigMapRef == nil {
		newNu := nu.DeepCopy()
		newNu.Spec.SetNode.Taints = append(newNu.Spec.SetNode.Taints, corev1.Taint{Key: KinsResourceNameSuffix, Effect: corev1.TaintEffectNoSchedule})
		newNu.Spec.UnitCredentialConfigMapRef = &corev1.ObjectReference{Namespace: DefaultKinsNamespace, Name: buildKinsConfigMapName(nu.Name), Kind: "ConfigMap"}
		if _, err := kc.crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), newNu, metav1.UpdateOptions{}); err != nil {
			klog.ErrorS(err, "Update nodeUnit setnode taints: %s error: %#v", nu.Name)
			return err
		}
	}

	return nil
}

func (kc *KinsController) recoverNodeUnit(nu *sitev1alpha2.NodeUnit) error {
	startTime := time.Now()
	klog.V(4).InfoS("Started remove nodeunit cluster", "nodeunit", nu.Name, "startTime", startTime)
	defer func() {
		klog.V(4).InfoS("Finished remove nodeunit cluster", "nodeunit", nu.Name, "duration", time.Since(startTime))
	}()

	// delete all resource
	unitResourceLabel := labels.SelectorFromSet(labels.Set(map[string]string{KinsResourceLabelKey: "yes", nu.Name: constant.NodeUnitSuperedge}))
	if err := kc.kubeClient.AppsV1().DaemonSets(DefaultKinsNamespace).DeleteCollection(
		context.TODO(),
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: unitResourceLabel.String()},
	); err != nil && !errors.IsNotFound(err) {
		klog.V(4).ErrorS(err, "Delete kins daemonset error", "node unit", nu.Name)
		return err
	}
	// delete service
	if err := kc.kubeClient.CoreV1().Services(DefaultKinsNamespace).Delete(
		context.TODO(),
		buildKinsServiceName(nu.Name),
		metav1.DeleteOptions{},
	); err != nil && !errors.IsNotFound(err) {
		klog.V(4).ErrorS(err, "Delete kins service error", "node unit", nu.Name)
		return err
	}
	// delete secret
	// do not delete secret default, or k3s server will error in restart
	if nu.Annotations[KinsUnitClusterClearAnno] == "yes" {
		if err := kc.kubeClient.CoreV1().Secrets(DefaultKinsNamespace).Delete(
			context.TODO(),
			buildKinsSecretName(nu.Name),
			metav1.DeleteOptions{},
		); err != nil && !errors.IsNotFound(err) {
			klog.V(4).ErrorS(err, "Delete kins secret error", "node unit", nu.Name)
			return err
		}
	}
	// delete configmap
	if err := kc.kubeClient.CoreV1().ConfigMaps(DefaultKinsNamespace).DeleteCollection(
		context.TODO(),
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: unitResourceLabel.String()},
	); err != nil && !errors.IsNotFound(err) {
		klog.V(4).ErrorS(err, "Delete kins secret error", "node unit", nu.Name)
		return err
	}

	// recover node unit setnode
	var newTaints []corev1.Taint
	for _, t := range nu.Spec.SetNode.Taints {
		if t.Key != KinsResourceNameSuffix && t.Effect != corev1.TaintEffectNoSchedule {
			newTaints = append(newTaints, t)
		}
	}

	if !reflect.DeepEqual(newTaints, nu.Spec.SetNode.Taints) || nu.Spec.UnitCredentialConfigMapRef != nil {
		newNu := nu.DeepCopy()
		newNu.Spec.SetNode.Taints = newTaints
		newNu.Spec.UnitCredentialConfigMapRef = nil
		if _, err := kc.crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), newNu, metav1.UpdateOptions{}); err != nil {
			klog.ErrorS(err, "Update nodeUnit setnode taints: %s error: %#v", nu.Name)
			return err
		}
	}

	return nil
}

func (kc *KinsController) UpdateUnitClusterStatus(nu *sitev1alpha2.NodeUnit) (*sitev1alpha2.UnitClusterStatus, error) {
	if nu.Spec.AutonomyLevel == sitev1alpha2.AutonomyLevelL3 {
		return &nu.Status.UnitCluster, nil
	}
	newStatus := nu.Status.UnitCluster.DeepCopy()
	kclient, err := BuildUnitClusterClientSet(kc.kubeClient, nu)
	if err != nil {
		return nil, err
	}
	// get version
	version, err := kclient.Discovery().ServerVersion()
	if err != nil {
		newStatus.Phase = sitev1alpha2.ClusterFailed
		klog.ErrorS(err, "get unit cluster version failed")
	} else {
		newStatus.Version = strings.TrimPrefix(version.String(), "v")
		newStatus.Phase = sitev1alpha2.ClusterRunning
	}

	return newStatus, nil
}

func buildKinsCRIDaemonSetName(nuName string) string {
	return fmt.Sprintf("%s-cri-%s", nuName, KinsResourceNameSuffix)
}

func buildKinsServerDaemonSetName(nuName string) string {
	return fmt.Sprintf("%s-server-%s", nuName, KinsResourceNameSuffix)
}

func buildKinsAgentDaemonSetName(nuName string) string {
	return fmt.Sprintf("%s-agent-%s", nuName, KinsResourceNameSuffix)
}

func buildKinsServiceName(nuName string) string {
	return fmt.Sprintf("%s-%s", nuName, KinsResourceNameSuffix)
}

func buildKinsConfigMapName(nuName string) string {
	return fmt.Sprintf("%s-%s", nuName, KinsResourceNameSuffix)
}

func buildKinsSecretName(nuName string) string {
	return fmt.Sprintf("%s-%s", nuName, KinsResourceNameSuffix)
}

func buildKinsServerEndpoint(ip string, port int) string {
	return fmt.Sprintf("%s://%s:%d", "https", ip, port)
}

func caculateKinsServiceCIDRAndCoreDNSIP(nu *sitev1alpha2.NodeUnit, existUnitCluster int32) (string, string) {
	defaultSvcCidr, defaultCorednsIP := fmt.Sprintf(DefaultKinsServiceCIDR, existUnitCluster, "0/16"), fmt.Sprintf(DefaultKinsServiceCIDR, existUnitCluster, "255")

	if nu.Spec.UnitClusterInfo != nil &&
		nu.Spec.UnitClusterInfo.Parameters != nil &&
		nu.Spec.UnitClusterInfo.Parameters[ParameterServiceCIDRKey] != "" {
		// TODO validate service cidr
		svcCidrStr := nu.Spec.UnitClusterInfo.Parameters[ParameterServiceCIDRKey]
		_, svcCidr, err := net.ParseCIDR(svcCidrStr)
		if err != nil {
			klog.ErrorS(err, "invalid service cidr", "service cidr string", svcCidrStr)
			return defaultSvcCidr, defaultCorednsIP
		}
		return svcCidrStr, getLastCIDRIPByIndex(svcCidr)
	}

	return defaultSvcCidr, defaultCorednsIP
}

// every unitcluster has 1000 nodeport,from 40000
func caculateKinsNodePortRange(nu *sitev1alpha2.NodeUnit, existUnitCluster int) string {
	if nu.Spec.UnitClusterInfo != nil &&
		nu.Spec.UnitClusterInfo.Parameters != nil &&
		nu.Spec.UnitClusterInfo.Parameters[ParameterNodePortRangeKey] != "" {
		// TODO validate node port range
		return nu.Spec.UnitClusterInfo.Parameters[ParameterNodePortRangeKey]
	}
	var start, end int

	start = DefaultKinsNodePortRangeStart + existUnitCluster*1000
	end = start + 1000

	return fmt.Sprintf("%d-%d", start, end)
}

func generateKinsSecretK3SToken(nuName string) string {
	hasher := sha1.New()
	hasher.Write([]byte(nuName))
	hasher.Write([]byte(rand.String(10)))
	return base64.URLEncoding.EncodeToString([]byte(hex.EncodeToString(hasher.Sum(nil))))
}

func generateKinsSecretKnownToken() string {
	return fmt.Sprintf("%s,admin,admin,system:masters", rand.String(32))
}

func getLastCIDRIPByIndex(ipnet *net.IPNet) string {
	// convert IPNet struct mask and address to uint32
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	start := binary.BigEndian.Uint32(ipnet.IP)

	// find the final address
	lastIP := (start & mask) | (mask ^ 0xffffffff)
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, lastIP)
	return ip.String()
}

func getK3SImage(nu *sitev1alpha2.NodeUnit) string {
	if nu.Spec.UnitClusterInfo != nil &&
		nu.Spec.UnitClusterInfo.Parameters != nil &&
		nu.Spec.UnitClusterInfo.Parameters[ParameterK3SImageKey] != "" {
		return nu.Spec.UnitClusterInfo.Parameters[ParameterK3SImageKey]
	}
	return DefaultK3SImage
}

func getCRIWImage(nu *sitev1alpha2.NodeUnit) string {
	if nu.Spec.UnitClusterInfo != nil &&
		nu.Spec.UnitClusterInfo.Parameters != nil &&
		nu.Spec.UnitClusterInfo.Parameters[ParameterCRIWImageKey] != "" {
		return nu.Spec.UnitClusterInfo.Parameters[ParameterCRIWImageKey]
	}
	return DefaultKinsCRIWImage
}

func BuildUnitClusterClientSet(k8sClientSet clientset.Interface, nu *sitev1alpha2.NodeUnit) (clientset.Interface, error) {
	if nu.Spec.UnitCredentialConfigMapRef != nil {
		cmName := nu.Spec.UnitCredentialConfigMapRef.Name
		cmNamespace := nu.Spec.UnitCredentialConfigMapRef.Namespace

		kubeconfigCM, err := k8sClientSet.CoreV1().ConfigMaps(cmNamespace).Get(context.TODO(), cmName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		kubeconfig, ok := kubeconfigCM.Data["kubeconfig.conf"]
		if ok {
			unitRestConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
			if err != nil {
				klog.ErrorS(err, "Failed to get restConfig", "nodeunit", nu.Name)
				return nil, err
			}
			unitKubeClient, err := kubernetes.NewForConfig(unitRestConfig)
			if err != nil {
				klog.ErrorS(err, "Failed to build unit cluster kube client", "nodeunit", nu.Name)
				return nil, err
			}
			return unitKubeClient, nil
		}
		klog.V(5).InfoS("Invalid UnitCredentialConfigMap for build clientset", "content", kubeconfigCM.String())
	}
	return nil, fmt.Errorf("Invalid node unit")
}

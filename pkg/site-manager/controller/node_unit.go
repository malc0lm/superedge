/*
Copyright 2021 The SuperEdge Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/superedge/superedge/pkg/site-manager/constant"
	deleter "github.com/superedge/superedge/pkg/site-manager/controller/deleter"
	"github.com/superedge/superedge/pkg/site-manager/controller/gc"

	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	crdinformers "github.com/superedge/superedge/pkg/site-manager/generated/informers/externalversions/site.superedge.io/v1alpha2"
	crdv1listers "github.com/superedge/superedge/pkg/site-manager/generated/listers/site.superedge.io/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	"k8s.io/apimachinery/pkg/util/wait"
	appinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	applisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sitev1alpha2 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/site-manager/utils"
)

type NodeUnitController struct {
	nodeLister       corelisters.NodeLister
	nodeListerSynced cache.InformerSynced

	dsListter      applisters.DaemonSetLister
	dsListerSynced cache.InformerSynced

	nodeUnitLister       crdv1listers.NodeUnitLister
	nodeUnitListerSynced cache.InformerSynced

	eventRecorder record.EventRecorder
	queue         workqueue.RateLimitingInterface
	kubeClient    clientset.Interface
	crdClient     *crdClientset.Clientset

	syncHandler     func(key string) error
	enqueueNodeUnit func(name string)
	nodeUnitDeleter *deleter.NodeUnitDeleter
	nodegc          *gc.NodeUnitGarbageCollector
}

func NewSitesManagerDaemonController(
	nodeInformer coreinformers.NodeInformer,
	dsInformer appinformers.DaemonSetInformer,
	nodeUnitInformer crdinformers.NodeUnitInformer,
	nodeGroupInformer crdinformers.NodeGroupInformer,
	kubeClient clientset.Interface,
	crdClient *crdClientset.Clientset,
) *NodeUnitController {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1.EventSinkImpl{
		Interface: kubeClient.CoreV1().Events(""),
	})

	nodeUnitController := &NodeUnitController{
		kubeClient:    kubeClient,
		crdClient:     crdClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "site-manager-daemon"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "site-manager-daemon"),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nodeUnitController.addNode,
		UpdateFunc: nodeUnitController.updateNode,
		DeleteFunc: nodeUnitController.deleteNode,
	})

	dsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nodeUnitController.addDaemonSet,
		UpdateFunc: nodeUnitController.updateDaemonSet,
		DeleteFunc: nodeUnitController.deleteDaemonSet,
	})

	nodeUnitInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nodeUnitController.addNodeUnit,
		UpdateFunc: nodeUnitController.updateNodeUnit,
		DeleteFunc: nodeUnitController.deleteNodeUnit,
	})

	nodeUnitController.syncHandler = nodeUnitController.syncUnit
	nodeUnitController.enqueueNodeUnit = nodeUnitController.enqueue

	nodeUnitController.nodeLister = nodeInformer.Lister()
	nodeUnitController.nodeListerSynced = nodeInformer.Informer().HasSynced

	nodeUnitController.nodeUnitLister = nodeUnitInformer.Lister()
	nodeUnitController.nodeUnitListerSynced = nodeUnitInformer.Informer().HasSynced

	nodeUnitController.nodeUnitDeleter = deleter.NewNodeUnitDeleter(kubeClient, crdClient, nodeInformer.Lister(), NodeUnitFinalizerID)
	klog.V(4).Infof("Site-manager set handler success")

	return nodeUnitController
}

func (c *NodeUnitController) Run(workers, syncPeriodAsWhole int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	klog.V(1).Infof("Starting site-manager daemon")
	defer klog.V(1).Infof("Shutting down site-manager daemon")

	if !cache.WaitForNamedCacheSync("site-manager-daemon", stopCh,
		c.nodeListerSynced, c.nodeUnitListerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
		klog.V(4).Infof("Site-manager set worker-%d success", i)
	}

	<-stopCh
}

func (c *NodeUnitController) worker() {
	for c.processNextWorkItem() {
	}
}

func (c *NodeUnitController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	klog.V(4).Infof("Get siteManager queue key: %s", key)
	err := c.syncHandler(key.(string))
	c.handleErr(err, key)

	return true
}

func (c *NodeUnitController) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}

	if c.queue.NumRequeues(key) < constant.MaxRetries {
		klog.V(2).Infof("Error syncing siteManager %v: %v", key, err)
		c.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping siteManager %q out of the queue: %v", key, err)
	c.queue.Forget(key)
}

func (c *NodeUnitController) addDaemonSet(obj interface{}) {
}
func (c *NodeUnitController) updateDaemonSet(oldObj interface{}, newObj interface{}) {
}
func (c *NodeUnitController) deleteDaemonSet(obj interface{}) {
}

func (c *NodeUnitController) addNodeUnit(obj interface{}) {
	nu := obj.(*sitev1alpha2.NodeUnit)
	klog.V(5).InfoS("Adding NodeUnit", "node unit", klog.KObj(nu))
	c.enqueueNodeUnit(nu.Name)
}
func (c *NodeUnitController) updateNodeUnit(oldObj interface{}, newObj interface{}) {
	oldNu, newNu := oldObj.(*sitev1alpha2.NodeUnit), newObj.(*sitev1alpha2.NodeUnit)
	klog.V(5).InfoS("Updating NodeUnit", "old node unit", klog.KObj(oldNu), "new node unit", klog.KObj(newNu))
	c.enqueueNodeUnit(newNu.Name)
}
func (c *NodeUnitController) deleteNodeUnit(obj interface{}) {

	nu, ok := obj.(*sitev1alpha2.NodeUnit)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		nu, ok = tombstone.Obj.(*sitev1alpha2.NodeUnit)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a NodeUnit %#v", obj))
			return
		}
	}
	klog.V(5).InfoS("Deleting NodeUnit", "node unit", klog.KObj(nu))
	c.enqueueNodeUnit(nu.Name)
}

func (c *NodeUnitController) addNode(obj interface{}) {
	node := obj.(*corev1.Node)
	if node.DeletionTimestamp != nil {
		c.deleteNode(obj)
		return
	}
	klog.V(5).InfoS("Adding Node", "node", klog.KObj(node))

	_, unitList, err := utils.GetUnitsByNode(c.nodeUnitLister, node)
	if err != nil {
		klog.V(2).ErrorS(err, "utils.GetUnitsByNode error", "node", node.Name)
		return
	}
	for _, nuName := range unitList {
		c.enqueueNodeUnit(nuName)
	}
	return
}
func (c *NodeUnitController) updateNode(oldObj, newObj interface{}) {
	oldNode, newNode := oldObj.(*corev1.Node), newObj.(*corev1.Node)
	if oldNode.ResourceVersion == newNode.ResourceVersion {
		// Periodic resync will send update events for all known nodes.
		return
	}
	klog.V(5).InfoS("Updating Node", "old node", klog.KObj(oldNode), "new node", klog.KObj(newNode))

	var oldUnitLabel, newUnitLabel map[string]string
	for k, v := range oldNode.Labels {
		if v == constant.NodeUnitSuperedge {
			oldUnitLabel[k] = v
		}
	}
	for k, v := range newNode.Labels {
		if v == constant.NodeUnitSuperedge {
			newUnitLabel[k] = v
		}
	}
	// maybe update node unit label manual, recover it
	if !reflect.DeepEqual(oldUnitLabel, newUnitLabel) {
		_, unitList, err := utils.GetUnitsByNode(c.nodeUnitLister, oldNode)
		if err != nil {
			klog.V(2).ErrorS(err, "utils.GetUnitsByNode error", "node", oldNode.Name)
			return
		}
		for _, nuName := range unitList {
			c.enqueueNodeUnit(nuName)
		}
	}
	// current unit enqueqe
	_, unitList, err := utils.GetUnitsByNode(c.nodeUnitLister, newNode)
	if err != nil {
		klog.V(2).ErrorS(err, "utils.GetUnitsByNode error", "node", newNode.Name)
		return
	}
	for _, nuName := range unitList {
		c.enqueueNodeUnit(nuName)
	}
	return
}
func (c *NodeUnitController) deleteNode(obj interface{}) {

	node, ok := obj.(*corev1.Node)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		node, ok = tombstone.Obj.(*corev1.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Node %#v", obj))
			return
		}
	}
	klog.V(5).InfoS("Deleting Node", "node", klog.KObj(node))

	_, unitList, err := utils.GetUnitsByNode(c.nodeUnitLister, node)
	if err != nil {
		klog.V(2).ErrorS(err, "utils.GetUnitsByNode error", "node", node.Name)
		return
	}
	for _, nuName := range unitList {
		c.enqueueNodeUnit(nuName)
	}
}

func (c *NodeUnitController) syncUnit(key string) error {
	startTime := time.Now()
	klog.V(4).InfoS("Started syncing nodeunit", "nodeunit", key, "startTime", startTime)
	defer func() {
		klog.V(4).InfoS("Finished syncing nodeunit", "nodeunit", key, "duration", time.Since(startTime))
	}()

	n, err := c.nodeUnitLister.Get(key)
	if errors.IsNotFound(err) {
		klog.V(2).InfoS("NodeUnit has been deleted", "nodeunit", key)
		// deal with node unit delete

		return nil
	}
	if err != nil {
		return err
	}

	nu := n.DeepCopy()

	if nu.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !controllerutil.ContainsFinalizer(nu, NodeUnitFinalizerID) {
			controllerutil.AddFinalizer(nu, NodeUnitFinalizerID)
			if _, err := c.crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), nu, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(nu, NodeUnitFinalizerID) {
			// our finalizer is present, so lets handle any external dependency
			if err := c.nodeUnitDeleter.Delete(context.TODO(), nu); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return err
			}

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(nu, NodeUnitFinalizerID)
			if _, err := c.crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), nu, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		// Stop reconciliation as the item is being deleted
		return nil
	}

	// reconcile
	return c.reconcileNodeUnit(nu)
}
func (c *NodeUnitController) enqueue(name string) {
	c.queue.Add(name)
}

// func (c *NodeUnitController) addNodeUnit(obj interface{}) {
// 	nodeUnit := obj.(*sitev1alpha2.NodeUnit)
// 	klog.V(4).Infof("Get Add nodeUnit: %s", util.ToJson(nodeUnit))
// 	if nodeUnit.DeletionTimestamp != nil {
// 		c.deleteNodeUnit(nodeUnit)
// 		return
// 	}

// 	readyNodes, notReadyNodes, err := utils.GetNodesByUnit(c.kubeClient, nodeUnit)
// 	if err != nil {
// 		if strings.Contains(err.Error(), "not found") {
// 			readyNodes, notReadyNodes = []string{}, []string{}
// 			klog.Warningf("Get unit: %s node nil", nodeUnit.Name)
// 		} else {
// 			klog.Errorf("Get NodeUnit Nodes error: %v", err)
// 			return
// 		}
// 	}

// 	nodeUnitStatus := &nodeUnit.Status
// 	nodeUnitStatus.ReadyNodes = readyNodes
// 	nodeUnitStatus.ReadyRate = fmt.Sprintf("%d/%d", len(readyNodes), len(readyNodes)+len(notReadyNodes))
// 	nodeUnitStatus.NotReadyNodes = notReadyNodes
// 	nodeUnit, err = c.crdClient.SiteV1alpha2().NodeUnits().UpdateStatus(context.TODO(), nodeUnit, metav1.UpdateOptions{})
// 	if err != nil {
// 		klog.Errorf("Update nodeUnit: %s error: %#v", nodeUnit.Name, err)
// 		return
// 	}
// 	// set NodeUnit name to node
// 	if nodeUnit.Spec.SetNode.Labels == nil {
// 		nodeUnit.Spec.SetNode.Labels = make(map[string]string)
// 	}
// 	nodeUnit.Spec.SetNode.Labels[nodeUnit.Name] = constant.NodeUnitSuperedge
// 	/*
// 	 nodeGroup action
// 	*/
// nodeGroups, err := utils.UnitMatchNodeGroups(c.kubeClient, c.crdClient, nodeUnit.Name)
// if err != nil {
// 	klog.Errorf("Get NodeGroups error: %v", err)
// 	return
// }

// 	// Update nodegroups
// 	for _, nodeGroup := range nodeGroups {
// 		nodeGroupStatus := &nodeGroup.Status
// 		nodeGroupStatus.NodeUnits = append(nodeGroupStatus.NodeUnits, nodeUnit.Name)
// 		nodeGroupStatus.NodeUnits = util.RemoveDuplicateElement(nodeGroupStatus.NodeUnits)
// 		nodeGroupStatus.UnitNumber = len(nodeGroupStatus.NodeUnits)
// 		_, err = c.crdClient.SiteV1alpha2().NodeGroups().UpdateStatus(context.TODO(), &nodeGroup, metav1.UpdateOptions{})
// 		if err != nil {
// 			klog.Errorf("Update nodeGroup: %s error: %#v", nodeGroup.Name, err)
// 			continue
// 		}
// 		nodeUnit.Spec.SetNode.Labels[nodeGroup.Name] = nodeUnit.Name
// 	}

// 	nodeUnit, err = c.crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), nodeUnit, metav1.UpdateOptions{})
// 	if err != nil && !errors.IsConflict(err) {
// 		klog.Errorf("Update nodeUnit: %s error: %#v", nodeUnit.Name, err)
// 		return
// 	}
// 	nodeNames := append(readyNodes, notReadyNodes...)
// 	utils.SetNodeToNodes(c.kubeClient, nodeUnit.Spec.SetNode, nodeNames)

// 	klog.V(4).Infof("Add nodeUnit success: %s", util.ToJson(nodeUnit))
// }

// func (c *NodeUnitController) updateNodeUnit(oldObj, newObj interface{}) {
// 	oldNodeUnit := oldObj.(*sitev1alpha2.NodeUnit)
// 	curNodeUnit := newObj.(*sitev1alpha2.NodeUnit)
// 	klog.V(4).Infof("Get oldNodeUnit: %s", util.ToJson(oldNodeUnit))
// 	klog.V(4).Infof("Get curNodeUnit: %s", util.ToJson(curNodeUnit))

// 	if oldNodeUnit.ResourceVersion == curNodeUnit.ResourceVersion {
// 		return
// 	}
// 	/*
// 		curNodeUnit
// 	*/
// 	readyNodes, notReadyNodes, err := utils.GetNodesByUnit(c.kubeClient, curNodeUnit)
// 	if err != nil {
// 		if strings.Contains(err.Error(), "not found") {
// 			readyNodes, notReadyNodes = []string{}, []string{}
// 			klog.Warningf("Get unit: %s node nil", curNodeUnit.Name)
// 		} else {
// 			klog.Errorf("Get NodeUnit Nodes error: %v", err)
// 			return
// 		}
// 	}

// 	nodeUnitStatus := &curNodeUnit.Status
// 	nodeUnitStatus.ReadyNodes = readyNodes
// 	nodeUnitStatus.ReadyRate = fmt.Sprintf("%d/%d", len(readyNodes), len(readyNodes)+len(notReadyNodes))
// 	nodeUnitStatus.NotReadyNodes = notReadyNodes
// 	curNodeUnit, err = c.crdClient.SiteV1alpha2().NodeUnits().UpdateStatus(context.TODO(), curNodeUnit, metav1.UpdateOptions{})
// 	if err != nil && !errors.IsConflict(err) {
// 		klog.Errorf("Update nodeUnit: %s error: %#v", curNodeUnit.Name, err)
// 		return
// 	}

// 	// new node add or old node remove
// 	nodeNamesCur := append(readyNodes, notReadyNodes...)
// 	nodeNamesOld := append(oldNodeUnit.Status.ReadyNodes, oldNodeUnit.Status.NotReadyNodes...)
// 	removeNodes, updateNodes := utils.NeedUpdateNode(nodeNamesOld, nodeNamesCur)
// 	klog.V(4).Infof("NodeUnit: %s need remove nodes: %s setNode: %s", oldNodeUnit.Name, util.ToJson(removeNodes), util.ToJson(oldNodeUnit.Spec.SetNode))
// 	if err := utils.RemoveSetNode(c.kubeClient, oldNodeUnit, removeNodes); err != nil {
// 		klog.Errorf("Remove node NodeUnit setNode error: %v", err)
// 		return
// 	}
// 	/*
// 	   nodeGroup action
// 	*/
// 	nodeGroups, err := utils.UnitMatchNodeGroups(c.kubeClient, c.crdClient, curNodeUnit.Name)
// 	if err != nil {
// 		klog.Errorf("Get NodeGroups error: %v", err)
// 		return
// 	}
// 	klog.V(4).Infof("NodeUnit: %s contain NodeGroup: %s", curNodeUnit.Name, util.ToJson(nodeGroups))

// 	// todo: old nodeGroup need remove from nodeUnit
// 	setNode := &curNodeUnit.Spec.SetNode
// 	if setNode.Labels == nil {
// 		setNode.Labels = make(map[string]string)
// 	}
// 	// Update nodegroups
// 	for _, nodeGroup := range nodeGroups {
// 		nodeGroupTemp := nodeGroup
// 		nodeGroupStatus := &nodeGroupTemp.Status
// 		nodeGroupStatus.NodeUnits = append(nodeGroupStatus.NodeUnits, curNodeUnit.Name)
// 		nodeGroupStatus.NodeUnits = util.RemoveDuplicateElement(nodeGroupStatus.NodeUnits)
// 		nodeGroupStatus.UnitNumber = len(nodeGroupStatus.NodeUnits)
// 		_, err = c.crdClient.SiteV1alpha2().NodeGroups().UpdateStatus(context.TODO(), &nodeGroup, metav1.UpdateOptions{})
// 		if err != nil {
// 			klog.Errorf("Update nodeGroup: %s error: %#v", nodeGroup.Name, err)
// 			continue
// 		}
// 		setNode.Labels[nodeGroup.Name] = curNodeUnit.Name
// 	}

// 	//todo setNode.Labels need update
// 	curNodeUnit, err = c.crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), curNodeUnit, metav1.UpdateOptions{})
// 	if err != nil && !errors.IsConflict(err) {
// 		klog.Errorf("Update nodeUnit: %s error: %#v", curNodeUnit.Name, err)
// 		return
// 	}

// 	klog.V(4).Infof("NodeUnit: %s oldSetNode: %v, curSetNode: %v", curNodeUnit.Name, oldNodeUnit.Spec.SetNode, curNodeUnit.Spec.SetNode)
// 	utils.UpdtateNodeFromSetNode(c.kubeClient, oldNodeUnit.Spec.SetNode, curNodeUnit.Spec.SetNode, updateNodes)

// 	klog.V(4).Infof("Update nodeUnit success: %s", util.ToJson(curNodeUnit))
// }

// func (c *NodeUnitController) deleteNodeUnit(obj interface{}) {
// 	nodeUnit, ok := obj.(*sitev1alpha2.NodeUnit)
// 	if !ok {
// 		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
// 		if !ok {
// 			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v\n", obj))
// 			return
// 		}
// 		nodeUnit, ok = tombstone.Obj.(*sitev1alpha2.NodeUnit)
// 		if !ok {
// 			utilruntime.HandleError(fmt.Errorf("Tombstone contained object is not a nodeUnit %#v\n", obj))
// 			return
// 		}
// 	}

// 	/*
// 	 nodeGroup action
// 	*/
// 	nodeGroups, err := utils.GetNodeGroupsByUnit(c.crdClient, nodeUnit.Name)
// 	if err != nil {
// 		klog.Errorf("Get NodeGroups error: %v", err)
// 		return
// 	}

// 	// Update nodegroups
// 	for _, nodeGroup := range nodeGroups {
// 		nodeGroupStatus := &nodeGroup.Status
// 		nodeGroupStatus.NodeUnits = util.DeleteSliceElement(nodeGroupStatus.NodeUnits, nodeUnit.Name)
// 		nodeGroupStatus.UnitNumber = len(nodeGroupStatus.NodeUnits)
// 		_, err := c.crdClient.SiteV1alpha1().NodeGroups().UpdateStatus(context.TODO(), &nodeGroup, metav1.UpdateOptions{})
// 		if err != nil {
// 			klog.Errorf("Update nodeGroup: %s error: %#v", nodeGroup.Name, err)
// 			continue
// 		}
// 		klog.V(4).Infof("Delete nodeUnit: %s from nodeGroup: %s success", nodeUnit.Name, nodeGroup.Name)
// 	}

// 	readyNodes, notReadyNodes, err := utils.GetNodesByUnit(c.kubeClient, nodeUnit)
// 	if err != nil {
// 		if strings.Contains(err.Error(), "not found") {
// 			klog.Warningf("Get unit: %s node nil", nodeUnit.Name)
// 			return
// 		} else {
// 			klog.Errorf("Get NodeUnit Nodes error: %v", err)
// 			return
// 		}
// 	}
// 	nodeNames := append(readyNodes, notReadyNodes...)
// 	utils.DeleteNodesFromSetNode(c.kubeClient, nodeUnit.Spec.SetNode, nodeNames)

// 	klog.V(4).Infof("Delete NodeUnit: %s succes.", nodeUnit.Name)
// }

func (c *NodeUnitController) reconcileNodeUnit(nu *sitev1alpha2.NodeUnit) error {

	// malc TODO: set node role? why?

	// 0. list nodemap and nodeset belong to current node unit

	unitNodeSet, nodeMap, err := utils.GetNodesByUnit(c.nodeLister, nu)
	// 1. check nodes which should not belong to this unit, clear them(this will use gc)
	currentNodeSet := sets.NewString()

	var currentNodeMap, gcNodeMap map[string]*corev1.Node
	currentNodeSelector := labels.NewSelector()
	nRequire, err := labels.NewRequirement(
		nu.Name,
		selection.In,
		[]string{constant.NodeUnitSuperedge},
	)
	if err != nil {
		return err
	}
	currentNodeSelector.Add(*nRequire)
	utils.ListNodeFromLister(c.nodeLister, currentNodeSelector, func(n interface{}) {
		node, ok := n.(*corev1.Node)
		if !ok {
			return
		}
		currentNodeMap[node.Name] = node
		currentNodeSet.Insert(node.Name)
	})

	// find need gc node set
	needGCNodes := currentNodeSet.Difference(unitNodeSet)
	for _, gcNode := range needGCNodes.UnsortedList() {
		gcNodeMap[gcNode] = currentNodeMap[gcNode]
	}
	if err := utils.DeleteNodesFromSetNode(c.kubeClient, nu, gcNodeMap); err != nil {
		return err
	}

	// 2. check node which should belong to this unit, ensure setNode(default is label)
	// 2 set node
	if err := utils.SetNodeToNodes(c.kubeClient, nu, nodeMap); err != nil {
		return err
	}
	// 3. caculate node unit status
	newStatus, err := utils.CaculateNodeUnitStatus(nodeMap, nu)
	if err != nil {
		return nil
	}
	nu.Status = *newStatus

	if !reflect.DeepEqual(*newStatus, nu.Status) {
		// update node unit status only when status changed
		_, err = c.crdClient.SiteV1alpha2().NodeUnits().UpdateStatus(context.TODO(), nu, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("Update nodeUnit: %s error: %#v", nu.Name, err)
			return err
		}
	}
	klog.V(5).Infof("NodeUnit=(%s) update success", nu.Name)

	return nil
}

// func (c *NodeUnitController) addNode(obj interface{}) {
// 	node := obj.(*corev1.Node)

// 	klog.V(4).Infof("Add a new node: %s", node.Name)

// 	// // set node role
// 	// if err := utils.SetNodeRole(c.kubeClient, node); err != nil {
// 	// 	klog.Errorf("Set node: %s role error: %#v", err)
// 	// }

// 	// 1. get all nodeunit
// 	allNodeUnit, err := c.crdClient.SiteV1alpha2().NodeUnits().List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		klog.Errorf("List nodeUnit error: %#v", err)
// 		return
// 	}

// 	// 2.node match nodeunit
// 	nodeLabels := node.Labels
// 	var setNode sitev1alpha2.SetNode
// 	setNodeLables := make(map[string]string)
// 	for _, nodeunit := range allNodeUnit.Items {
// 		var matchName bool = false
// 		// Selector match
// 		nodeunitSelector := nodeunit.Spec.Selector
// 		if nodeunitSelector != nil && nodeunitSelector.MatchLabels != nil {
// 			var matchNum int = 0
// 			for key, value := range nodeunitSelector.MatchLabels { //todo: MatchExpressions && Annotations
// 				labelsValue, ok := nodeLabels[key]
// 				if !ok || labelsValue != value {
// 					break
// 				}
// 				if ok && labelsValue == value {
// 					matchNum++
// 				}
// 			}
// 			if len(nodeunitSelector.MatchLabels) == matchNum {
// 				matchName = true
// 			}
// 		}

// 		// nodeName match
// 		for _, nodeName := range nodeunit.Spec.Nodes {
// 			if nodeName == node.Name {
// 				matchName = true
// 				break
// 			}
// 		}

// 		klog.V(4).Infof("Node: %s is match: %v NodeUnit: %s", node.Name, matchName, nodeunit.Name)
// 		if matchName {
// 			unitStatus := &nodeunit.Status
// 			if utilkube.IsReadyNode(node) {
// 				unitStatus.ReadyNodes = append(unitStatus.ReadyNodes, node.Name)
// 				unitStatus.ReadyNodes = util.RemoveDuplicateElement(unitStatus.ReadyNodes)
// 			} else {
// 				unitStatus.NotReadyNodes = append(unitStatus.NotReadyNodes, node.Name)
// 				unitStatus.NotReadyNodes = util.RemoveDuplicateElement(unitStatus.NotReadyNodes)
// 			}
// 			unitStatus.ReadyRate = utils.AddNodeUitReadyRate(&nodeunit)

// 			_, err = c.crdClient.SiteV1alpha2().NodeUnits().UpdateStatus(context.TODO(), &nodeunit, metav1.UpdateOptions{})
// 			if err != nil && !errors.IsConflict(err) {
// 				klog.Errorf("Update nodeUnit: %s error: %#v", nodeunit.Name, err)
// 				return
// 			}
// 			if nodeunit.Spec.SetNode.Labels != nil {
// 				for key, value := range nodeunit.Spec.SetNode.Labels {
// 					setNodeLables[key] = value
// 				}
// 				setNodeLables[nodeunit.Name] = constant.NodeUnitSuperedge
// 			}
// 		}
// 		setNode.Labels = setNodeLables
// 	}

// 	allNodeGroup, err := c.crdClient.SiteV1alpha1().NodeGroups().List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		klog.Errorf("List nodeGroup error: %#v", err)
// 		return
// 	}
// 	for _, ng := range allNodeGroup.Items {
// 		if len(ng.Spec.AutoFindNodeKeys) == 0 {
// 			return
// 		}

// 		if len(node.Labels) < len(ng.Spec.AutoFindNodeKeys) {
// 			return
// 		}
// 		// sort autofindkeys

// 		// generate label keys to slice
// 		keys := make([]string, len(node.Labels))
// 		for k := range node.Labels {
// 			keys = append(keys, k)
// 		}

// 	}

// 	utils.SetNodeToNodes(c.kubeClient, setNode, []string{node.Name})
// 	klog.V(1).Infof("Add node: %s to all match node-unit success.", node.Name)
// }

// func (c *NodeUnitController) updateNode(oldObj, newObj interface{}) {
// 	oldNode, curNode := oldObj.(*corev1.Node), newObj.(*corev1.Node)
// 	if curNode.ResourceVersion == oldNode.ResourceVersion {
// 		return
// 	}
// 	klog.V(4).Infof("Get oldNode: %s", util.ToJson(oldNode))
// 	klog.V(4).Infof("Get curNode: %s", util.ToJson(curNode))

// 	// set node role
// 	if err := utils.SetNodeRole(c.kubeClient, curNode); err != nil {
// 		klog.Errorf("Set node: %s role error: %#v", err)
// 	}

// 	//nodeUnits, err := utils.GetUnitsByNode(c.crdClient, curNode)
// 	//if err != nil {
// 	//	klog.Errorf("Get nodeUnit by node, error： %#v", err)
// 	//	return
// 	//}

// 	// 1. get all nodeunit
// 	allNodeUnit, err := c.crdClient.SiteV1alpha2().NodeUnits().List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		klog.Errorf("List nodeUnit error: %#v", err)
// 		return
// 	}

// 	// 2.node match nodeunit
// 	nodeLabels := curNode.Labels
// 	for _, nodeunit := range allNodeUnit.Items {
// 		var matchName bool = false
// 		// Selector match
// 		nodeunitSelector := nodeunit.Spec.Selector
// 		if nodeunitSelector != nil && nodeunitSelector.MatchLabels != nil {
// 			var matchNum int = 0
// 			for key, value := range nodeunitSelector.MatchLabels { //todo: MatchExpressions && Annotations
// 				labelsValue, ok := nodeLabels[key]
// 				if !ok || labelsValue != value {
// 					break
// 				}
// 				if ok && labelsValue == value {
// 					matchNum++
// 				}
// 			}
// 			if len(nodeunitSelector.MatchLabels) == matchNum {
// 				matchName = true
// 			}
// 		}

// 		// nodeName match
// 		for _, nodeName := range nodeunit.Spec.Nodes {
// 			if nodeName == curNode.Name {
// 				matchName = true
// 				break
// 			}
// 		}

// 		klog.V(4).Infof("Node: %s is match: %v NodeUnit: %s", curNode.Name, matchName, nodeunit.Name)
// 		if matchName {
// 			var nodeUnit = nodeunit
// 			unitStatus := &nodeUnit.Status
// 			if utilkube.IsReadyNode(oldNode) {
// 				unitStatus.NotReadyNodes = util.DeleteSliceElement(unitStatus.NotReadyNodes, curNode.Name)
// 				unitStatus.ReadyNodes = append(unitStatus.ReadyNodes, curNode.Name)
// 				unitStatus.ReadyNodes = util.RemoveDuplicateElement(unitStatus.ReadyNodes)
// 			}
// 			if !utilkube.IsReadyNode(oldNode) {
// 				unitStatus.ReadyNodes = util.DeleteSliceElement(unitStatus.ReadyNodes, curNode.Name)
// 				unitStatus.NotReadyNodes = append(unitStatus.NotReadyNodes, curNode.Name)
// 				unitStatus.ReadyNodes = util.RemoveDuplicateElement(unitStatus.NotReadyNodes)
// 			}
// 			unitStatus.ReadyRate = utils.GetNodeUitReadyRate(&nodeUnit)

// 			_, err = c.crdClient.SiteV1alpha2().NodeUnits().UpdateStatus(context.TODO(), &nodeUnit, metav1.UpdateOptions{})
// 			if err != nil && !errors.IsConflict(err) {
// 				klog.Errorf("Update nodeUnit: %s error: %#v", nodeUnit.Name, err)
// 				return
// 			}
// 			klog.V(4).Infof("Node: %s join nodeUnit: %s success.", curNode, nodeUnit.Name)
// 		}
// 	}
// 	/*
// 		todo: if update node annotations, such as nodeunit annotations deleted
// 	*/

// 	klog.V(4).Infof("Node: %s status update with update nodeUnit success", curNode.Name)
// }

// func (c *NodeUnitController) deleteNode(obj interface{}) {

// 	node, ok := obj.(*corev1.Node)
// 	if !ok {
// 		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
// 		if !ok {
// 			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v\n", obj))
// 			return
// 		}
// 		node, ok = tombstone.Obj.(*corev1.Node)
// 		if !ok {
// 			utilruntime.HandleError(fmt.Errorf("Tombstone contained object is not a node %#v\n", obj))
// 			return
// 		}
// 	}
// 	klog.V(5).Infof("Node: %s was deleted", node.Name)

// 	// get all node unit which contain this node and enqueue NodeUnit
// 	nodeUnits, err := utils.GetUnitsByNode(c.crdClient, node)
// 	if err != nil {
// 		klog.Errorf("Get nodeUnit by node, error: %#v", err)
// 		return
// 	}
// 	for _, nu := range nodeUnits {
// 		klog.V(5).Infof("Node: %s was deleted and node unit %s enqueue", node.Name, nu.Name)
// 		c.enqueueNodeUnit(nu.Name)
// 	}

// 	// malc TODO: delete it

// 	// for _, nodeUnit := range nodeUnits {
// 	// 	unitStatus := &nodeUnit.Status
// 	// 	unitStatus.ReadyNodes = util.DeleteSliceElement(unitStatus.ReadyNodes, node.Name)
// 	// 	unitStatus.ReadyNodes = util.RemoveDuplicateElement(unitStatus.ReadyNodes)
// 	// 	unitStatus.NotReadyNodes = util.DeleteSliceElement(unitStatus.NotReadyNodes, node.Name)
// 	// 	unitStatus.NotReadyNodes = util.RemoveDuplicateElement(unitStatus.NotReadyNodes)
// 	// 	unitStatus.ReadyRate = utils.GetNodeUitReadyRate(&nodeUnit)
// 	// 	_, err = c.crdClient.SiteV1alpha2().NodeUnits().UpdateStatus(context.TODO(), &nodeUnit, metav1.UpdateOptions{})
// 	// 	if err != nil && !errors.IsConflict(err) {
// 	// 		klog.Errorf("Update nodeUnit: %s error: %#v", nodeUnit.Name, err)
// 	// 		return
// 	// 	}
// 	// 	klog.V(6).Infof("Updated nodeUnit: %s success", nodeUnit.Name)
// 	// }
// }

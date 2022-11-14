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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
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

	"github.com/pingcap/errors"
	sitev1alpha2 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	deleter "github.com/superedge/superedge/pkg/site-manager/controller/deleter"

	"github.com/superedge/superedge/pkg/site-manager/constant"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	crdinformers "github.com/superedge/superedge/pkg/site-manager/generated/informers/externalversions/site.superedge.io/v1alpha2"
	crdv1listers "github.com/superedge/superedge/pkg/site-manager/generated/listers/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/site-manager/utils"
	"github.com/superedge/superedge/pkg/util"
)

type NodeGroupController struct {
	nodeLister       corelisters.NodeLister
	nodeListerSynced cache.InformerSynced

	dsListter      applisters.DaemonSetLister
	dsListerSynced cache.InformerSynced

	nodeUnitLister       crdv1listers.NodeUnitLister
	nodeUnitListerSynced cache.InformerSynced

	nodeGroupLister       crdv1listers.NodeGroupLister
	nodeGroupListerSynced cache.InformerSynced

	eventRecorder record.EventRecorder
	queue         workqueue.RateLimitingInterface
	kubeClient    clientset.Interface
	crdClient     *crdClientset.Clientset

	syncHandler      func(key string) error
	enqueueNodeGroup func(nu *sitev1alpha2.NodeGroup)
	nodeGroupDeleter *deleter.NodeGroupDeleter
}

func NewNodeGroupController(
	nodeInformer coreinformers.NodeInformer,
	dsInformer appinformers.DaemonSetInformer,
	nodeUnitInformer crdinformers.NodeUnitInformer,
	nodeGroupInformer crdinformers.NodeGroupInformer,
	kubeClient clientset.Interface,
	crdClient *crdClientset.Clientset,
) *NodeGroupController {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1.EventSinkImpl{
		Interface: kubeClient.CoreV1().Events(""),
	})

	groupController := &NodeGroupController{
		kubeClient:    kubeClient,
		crdClient:     crdClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "site-manager-daemon"}),
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "site-manager-daemon"),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    groupController.addNode,
		UpdateFunc: groupController.updateNode,
		DeleteFunc: groupController.deleteNode,
	})

	dsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    groupController.addDaemonSet,
		UpdateFunc: groupController.updateDaemonSet,
		DeleteFunc: groupController.deleteDaemonSet,
	})

	nodeUnitInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    groupController.addNodeUnit,
		UpdateFunc: groupController.updateNodeUnit,
		DeleteFunc: groupController.deleteNodeUnit,
	})

	nodeGroupInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    groupController.addNodeGroup,
		UpdateFunc: groupController.updateNodeGroup,
		DeleteFunc: groupController.deleteNodeGroup,
	})

	groupController.syncHandler = groupController.syncUnit
	groupController.enqueueNodeGroup = groupController.enqueue

	groupController.nodeLister = nodeInformer.Lister()
	groupController.nodeListerSynced = nodeInformer.Informer().HasSynced

	groupController.nodeUnitLister = nodeUnitInformer.Lister()
	groupController.nodeUnitListerSynced = nodeUnitInformer.Informer().HasSynced

	groupController.nodeGroupLister = nodeGroupInformer.Lister()
	groupController.nodeGroupListerSynced = nodeGroupInformer.Informer().HasSynced

	klog.V(4).Infof("Site-manager set handler success")

	return groupController
}

func (c *NodeGroupController) Run(workers, syncPeriodAsWhole int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	klog.V(1).Infof("Starting site-manager daemon")
	defer klog.V(1).Infof("Shutting down site-manager daemon")

	if !cache.WaitForNamedCacheSync("site-manager-daemon", stopCh,
		c.nodeListerSynced, c.nodeUnitListerSynced, c.nodeGroupListerSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
		klog.V(4).Infof("Site-manager set worker-%d success", i)
	}

	<-stopCh
}

func (c *NodeGroupController) worker() {
	for c.processNextWorkItem() {
	}
}

func (c *NodeGroupController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	klog.V(4).Infof("Get siteManager queue key: %s", key)

	c.handleErr(nil, key)

	return true
}

func (c *NodeGroupController) handleErr(err error, key interface{}) {
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

func (siteManager *NodeGroupController) addNodeGroup(obj interface{}) {
	nodeGroup := obj.(*sitev1alpha2.NodeGroup)
	klog.V(4).Infof("Get Add nodeGroup: %s", util.ToJson(nodeGroup))
	if nodeGroup.DeletionTimestamp != nil {
		siteManager.deleteNodeGroup(nodeGroup) //todo
		return
	}

	if len(nodeGroup.Finalizers) == 0 {
		nodeGroup.Finalizers = append(nodeGroup.Finalizers, NodeGroupFinalizerID)
	}

	if len(nodeGroup.Spec.AutoFindNodeKeys) > 0 {
		utils.AutoFindNodeKeysbyNodeGroup(siteManager.kubeClient, siteManager.crdClient, nodeGroup)
	}

	units, err := utils.GetUnitsByNodeGroup(siteManager.kubeClient, siteManager.crdClient, nodeGroup)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			units = []string{}
			klog.Warningf("Get NodeGroup: %s unit nil", nodeGroup.Name)
		} else {
			klog.Errorf("Get NodeGroup unit error: %v", err)
			return
		}
	}

	nodeGroup.Status.NodeUnits = units
	nodeGroup.Status.UnitNumber = len(units)
	_, err = siteManager.crdClient.SiteV1alpha2().NodeGroups().UpdateStatus(context.TODO(), nodeGroup, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Update nodeGroup: %s error: %#v", nodeGroup.Name, err)
		return
	}

	klog.V(4).Infof("Add nodeGroup: %s success.", nodeGroup.Name)
}

func (siteManager *NodeGroupController) updateNodeGroup(oldObj, newObj interface{}) {
	oldNodeGroup := oldObj.(*sitev1alpha2.NodeGroup)
	curNodeGroup := newObj.(*sitev1alpha2.NodeGroup)
	klog.V(4).Infof("Get oldNodeGroup: %s", util.ToJson(oldNodeGroup))
	klog.V(4).Infof("Get curNodeGroup: %s", util.ToJson(curNodeGroup))

	if len(curNodeGroup.Finalizers) == 0 {
		curNodeGroup.Finalizers = append(curNodeGroup.Finalizers, NodeGroupFinalizerID)
	}

	if curNodeGroup.DeletionTimestamp != nil {
		siteManager.deleteNodeGroup(curNodeGroup) //todo
		return
	}

	if oldNodeGroup.ResourceVersion == curNodeGroup.ResourceVersion {
		return
	}

	if len(curNodeGroup.Spec.AutoFindNodeKeys) > 0 {
		utils.AutoFindNodeKeysbyNodeGroup(siteManager.kubeClient, siteManager.crdClient, curNodeGroup)
	}
	/*
		curNodeGroup
	*/

	units, err := utils.GetUnitsByNodeGroup(siteManager.kubeClient, siteManager.crdClient, curNodeGroup)
	if err != nil {
		klog.Errorf("Get NodeGroup unit error: %v", err)
		return
	}
	klog.V(4).Infof("NodeGroup: %s select nodeUnits: %v", curNodeGroup.Name, units)

	curNodeGroup.Status.NodeUnits = units
	curNodeGroup.Status.UnitNumber = len(units)
	curNodeGroup, err = siteManager.crdClient.SiteV1alpha2().NodeGroups().UpdateStatus(context.TODO(), curNodeGroup, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Update nodeGroup: %s error: %#v", curNodeGroup.Name, err)
		return
	}

	// reomve old nodegroup label
	var removeUnit []string
	unitMap := make(map[string]bool)
	for _, unit := range units {
		unitMap[unit] = true
	}
	for _, unit := range oldNodeGroup.Status.NodeUnits {
		if !unitMap[unit] {
			removeUnit = append(removeUnit, unit) //todo: more to do
		}
	}
	utils.RemoveUnitSetNode(siteManager.crdClient, removeUnit, []string{curNodeGroup.Name})

	klog.V(4).Infof("Updated nodeGroup: %s success", util.ToJson(curNodeGroup))
}

func (siteManager *NodeGroupController) deleteNodeGroup(obj interface{}) {
	nodeGroup, ok := obj.(*sitev1alpha2.NodeGroup)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v\n", obj))
			return
		}
		nodeGroup, ok = tombstone.Obj.(*sitev1alpha2.NodeGroup)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object is not a nodeGroup %#v\n", obj))
			return
		}
	}

	// check all nodes, if which have the label with nodegroup name then remove
	for _, nu := range nodeGroup.Status.NodeUnits {
		nodeUnit, err := siteManager.crdClient.SiteV1alpha1().NodeUnits().Get(context.TODO(), nu, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("List nodeUnit error: %#v", err)
			continue
		}
		if nodeUnit.Spec.SetNode.Labels != nil {
			delete(nodeUnit.Spec.SetNode.Labels, nodeGroup.Name)
		}

		_, err = siteManager.crdClient.SiteV1alpha1().NodeUnits().Update(context.TODO(), nodeUnit, metav1.UpdateOptions{})
		if err != nil {
			klog.Error("Update nodeunit fail ", err)
		}
	}

	klog.V(4).Infof("Delete NodeGroup: %s succes.", nodeGroup.Name)
	return
}

func (c *NodeGroupController) syncUnit(key string) error {
	startTime := time.Now()
	klog.V(4).InfoS("Started syncing nodeunit", "nodeunit", key, "startTime", startTime)
	defer func() {
		klog.V(4).InfoS("Finished syncing nodeunit", "nodeunit", key, "duration", time.Since(startTime))
	}()

	n, err := c.nodeGroupLister.Get(key)
	if errors.IsNotFound(err) {
		klog.V(2).InfoS("NodeUnit has been deleted", "nodeunit", key)
		// deal with node unit delete

		return nil
	}
	if err != nil {
		return err
	}

	ng := n.DeepCopy()

	if ng.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object. This is equivalent
		// registering our finalizer.
		if !controllerutil.ContainsFinalizer(ng, NodeGroupFinalizerID) {
			controllerutil.AddFinalizer(ng, NodeGroupFinalizerID)
			if _, err := c.crdClient.SiteV1alpha2().NodeGroups().Update(context.TODO(), ng, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(ng, NodeGroupFinalizerID) {
			// our finalizer is present, so lets handle any external dependency
			if err := c.nodeGroupDeleter.Delete(context.TODO(), ng); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return err
			}

			// remove our finalizer from the list and update it.
			controllerutil.RemoveFinalizer(ng, NodeGroupFinalizerID)
			if _, err := c.crdClient.SiteV1alpha2().NodeGroups().Update(context.TODO(), ng, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
		// Stop reconciliation as the item is being deleted
		return nil
	}

	// reconcile

	return nil
}
func (c *NodeGroupController) enqueue(nu *sitev1alpha2.NodeGroup) {
	key, err := KeyFunc(nu)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", nu, err))
		return
	}

	c.queue.Add(key)
}

func (c *NodeGroupController) addDaemonSet(obj interface{}) {
}
func (c *NodeGroupController) updateDaemonSet(oldObj interface{}, newObj interface{}) {
}
func (c *NodeGroupController) deleteDaemonSet(obj interface{}) {
}

func (c *NodeGroupController) addNode(obj interface{}) {
}
func (c *NodeGroupController) updateNode(oldObj interface{}, newObj interface{}) {
}
func (c *NodeGroupController) deleteNode(obj interface{}) {
}

func (c *NodeGroupController) addNodeUnit(obj interface{}) {
}
func (c *NodeGroupController) updateNodeUnit(oldObj interface{}, newObj interface{}) {
}
func (c *NodeGroupController) deleteNodeUnit(obj interface{}) {
}

package utils

import (
	"context"
	"time"

	sitev1alpha2 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	"github.com/superedge/superedge/pkg/util"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/klog/v2"
)

const (
	AllNodeUnit    = "unit-node-all"
	EdgeNodeUnit   = "unit-node-edge"
	CloudNodeUnit  = "unit-node-cloud"
	MasterNodeUnit = "unit-node-master"
)

func CreateDefaultUnit(ctx context.Context, crdClient *crdClientset.Clientset) error {
	// All Node Unit
	allNodeUnit := &sitev1alpha2.NodeUnit{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "site.superedge.io/v1alpha2",
			Kind:       "NodeUnit",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: AllNodeUnit,
		},
		Spec: sitev1alpha2.NodeUnitSpec{
			Type: sitev1alpha2.OtherNodeUnit,
			Selector: &sitev1alpha2.Selector{
				MatchLabels: map[string]string{
					"kubernetes.io/os": "linux",
				},
			},
		},
	}

	if _, err := crdClient.SiteV1alpha2().NodeUnits().Create(ctx, allNodeUnit, metav1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}

	return nil
}

func Migrator_v1alpha1_NodeUnit_To_v1alpha2_NodeUnit(ctx context.Context, crdClient *crdClientset.Clientset) error {

	a1NuList, err := crdClient.SiteV1alpha1().NodeUnits().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	// check v1alpha2 version is ready
	wait.Poll(
		wait.Jitter(2*time.Second, 1),
		2*time.Minute,
		func() (done bool, err error) {
			if _, err := crdClient.SiteV1alpha2().NodeUnits().List(context.TODO(), metav1.ListOptions{}); err == nil {
				return true, nil
			}
			return false, nil
		},
	)

	for _, a1nu := range a1NuList.Items {
		a2nu := sitev1alpha2.NodeUnit{
			ObjectMeta: metav1.ObjectMeta{
				Name:              a1nu.Name,
				Labels:            a1nu.Labels,
				Annotations:       a1nu.Annotations,
				Finalizers:        a1nu.Finalizers,
				OwnerReferences:   a1nu.OwnerReferences,
				ResourceVersion:   a1nu.ResourceVersion,
				UID:               a1nu.UID,
				CreationTimestamp: a1nu.CreationTimestamp,
			},
			Spec: sitev1alpha2.NodeUnitSpec{
				Type:          sitev1alpha2.NodeUnitType(a1nu.Spec.Type),
				Unschedulable: a1nu.Spec.Unschedulable,
				Nodes:         a1nu.Spec.Nodes,
				Selector: &sitev1alpha2.Selector{
					MatchLabels:      a1nu.Spec.Selector.MatchLabels,
					MatchExpressions: a1nu.Spec.Selector.MatchExpressions,
					Annotations:      a1nu.Spec.Selector.Annotations,
				},
				SetNode: sitev1alpha2.SetNode{
					Labels:      a1nu.Spec.SetNode.Labels,
					Annotations: a1nu.Spec.SetNode.Annotations,
					Taints:      a1nu.Spec.SetNode.Taints,
				},
				AutonomyLevel: sitev1alpha2.AutonomyLevelL3,
			},
		}
		klog.V(6).InfoS("migrate nodeunit v1alpha1 to v1alpha2", "old", util.ToJson(a1nu), "new", util.ToJson(a2nu))
		if _, err := crdClient.SiteV1alpha2().NodeUnits().Update(ctx, &a2nu, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func Migrator_v1alpha1_NodeGroup_To_v1alpha2_NodeGroup(ctx context.Context, crdClient *crdClientset.Clientset) error {
	a1NgList, err := crdClient.SiteV1alpha1().NodeGroups().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	// check v1alpha2 version is ready
	wait.Poll(
		wait.Jitter(2*time.Second, 1),
		2*time.Minute,
		func() (done bool, err error) {
			if _, err := crdClient.SiteV1alpha2().NodeGroups().List(context.TODO(), metav1.ListOptions{}); err == nil {
				return true, nil
			}
			return false, nil
		},
	)
	for _, a1ng := range a1NgList.Items {
		a2ng := sitev1alpha2.NodeGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:              a1ng.Name,
				Labels:            a1ng.Labels,
				Annotations:       a1ng.Annotations,
				Finalizers:        a1ng.Finalizers,
				OwnerReferences:   a1ng.OwnerReferences,
				ResourceVersion:   a1ng.ResourceVersion,
				UID:               a1ng.UID,
				CreationTimestamp: a1ng.CreationTimestamp,
			},
			Spec: sitev1alpha2.NodeGroupSpec{
				NodeUnits: a1ng.Spec.NodeUnits,
				Selector: &sitev1alpha2.Selector{
					MatchLabels:      a1ng.Spec.Selector.MatchLabels,
					MatchExpressions: a1ng.Spec.Selector.MatchExpressions,
					Annotations:      a1ng.Spec.Selector.Annotations,
				},
				AutoFindNodeKeys: a1ng.Spec.AutoFindNodeKeys,
			},
		}
		klog.V(6).InfoS("migrate nodegroup v1alpha1 to v1alpha2", "old", util.ToJson(a1ng), "new", util.ToJson(a2ng))
		if _, err := crdClient.SiteV1alpha2().NodeGroups().Update(ctx, &a2ng, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func InitAllRosource(ctx context.Context, crdClient *crdClientset.Clientset) error {
	if err := Migrator_v1alpha1_NodeUnit_To_v1alpha2_NodeUnit(ctx, crdClient); err != nil {
		klog.ErrorS(err, "Migrator_v1alpha1_NodeUnit_To_v1alpha2_NodeUnit error")
		return err
	}
	if err := Migrator_v1alpha1_NodeGroup_To_v1alpha2_NodeGroup(ctx, crdClient); err != nil {
		klog.ErrorS(err, "Migrator_v1alpha1_NodeGroup_To_v1alpha2_NodeGroup error")
		return err
	}
	if err := CreateDefaultUnit(ctx, crdClient); err != nil {
		klog.ErrorS(err, "create default unit error")
		return err
	}
	return nil
}

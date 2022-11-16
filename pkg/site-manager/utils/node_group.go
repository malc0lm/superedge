/*
Copyright 2022 The SuperEdge Authors.

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

package utils

// func GetUnitsByNodeGroup(kubeClient clientset.Interface, siteClient *siteClientset.Clientset, nodeGroup *v1alpha2.NodeGroup) (nodeUnits []string, err error) {
// 	// Get units by selector
// 	var unitList *v1alpha1.NodeUnitList
// 	selector := nodeGroup.Spec.Selector
// 	if selector != nil {
// 		if len(selector.MatchLabels) > 0 || len(selector.MatchExpressions) > 0 {
// 			labelSelector := &metav1.LabelSelector{
// 				MatchLabels:      selector.MatchLabels,
// 				MatchExpressions: selector.MatchExpressions,
// 			}
// 			selector, err := metav1.LabelSelectorAsSelector(labelSelector)
// 			if err != nil {
// 				return nodeUnits, err
// 			}

// 			listOptions := metav1.ListOptions{LabelSelector: selector.String()}
// 			unitList, err = siteClient.SiteV1alpha1().NodeUnits().List(context.TODO(), listOptions)
// 			if err != nil {
// 				klog.Errorf("Get nodes by selector, error: %v", err)
// 				return nodeUnits, err
// 			}
// 		}

// 		if len(selector.Annotations) > 0 { //todo: add Annotations selector

// 		}

// 		for _, unit := range unitList.Items {
// 			nodeUnits = append(nodeUnits, unit.Name)
// 		}
// 	}
// 	klog.V(4).Infof("NodeGroup: %s selector match after nodeUnits: %v", nodeGroup.Name, nodeUnits)

// 	// Get units by nodeName
// 	unitsNames := nodeGroup.Spec.NodeUnits
// 	for _, unitName := range unitsNames {
// 		unit, err := siteClient.SiteV1alpha2().NodeUnits().Get(context.TODO(), unitName, metav1.GetOptions{})
// 		if err != nil {
// 			klog.Errorf("Get unit by nodeGroup, error: %v", err)
// 			continue
// 		}
// 		nodeUnits = append(nodeUnits, unit.Name)
// 	}
// 	klog.V(4).Infof("NodeGroup: %s UnitName match after nodeUnits: %v", nodeGroup.Name, nodeUnits)

// 	copyNodeUnits := make([]string, len(nodeUnits))
// 	copy(copyNodeUnits, nodeUnits)
// 	UpdateNodeLabels(kubeClient, siteClient, copyNodeUnits, nodeGroup.Name)

// 	if len(nodeGroup.Spec.AutoFindNodeKeys) > 0 {
// 		nulist, err := siteClient.SiteV1alpha1().NodeUnits().List(context.TODO(), metav1.ListOptions{
// 			LabelSelector: fmt.Sprintf("%s=%s", nodeGroup.Name, constant.NodeUnitAutoFindLabelValue),
// 		})
// 		if err != nil {
// 			klog.Errorf("Get unit by nodeGroup, error: %v", err)
// 			return nil, err
// 		}

// 		for _, nu := range nulist.Items {
// 			nodeUnits = append(nodeUnits, nu.Name)
// 		}
// 	}
// 	klog.V(4).Infof("NodeGroup: %s autoFindNodeKeys match after nodeUnits: %v", nodeGroup.Name, nodeUnits)

// 	return util.RemoveDuplicateElement(nodeUnits), nil
// }

// func GetNodeGroupsByUnit(siteClient *siteClientset.Clientset, unitName string) (nodeGroups []v1alpha1.NodeGroup, err error) {
// 	allNodeGroups, err := siteClient.SiteV1alpha1().NodeGroups().List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		klog.Errorf("Get nodeGroup by unit, error: %v", err)
// 		return nil, err
// 	}

// 	for _, nodeGroup := range allNodeGroups.Items {
// 		for _, unit := range nodeGroup.Status.NodeUnits {
// 			if unit == unitName {
// 				nodeGroups = append(nodeGroups, nodeGroup)
// 			}
// 		}
// 	}
// 	return nodeGroups, nil
// }

// func UnitMatchNodeGroups(kubeClient clientset.Interface, siteClient *siteClientset.Clientset, unitName string) (nodeGroups []v1alpha2.NodeGroup, err error) {
// 	allNodeGroups, err := siteClient.SiteV1alpha2().NodeGroups().List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		klog.Errorf("Get nodeGroup by unit, error: %v", err)
// 		return nil, err
// 	}

// 	for _, nodeGroup := range allNodeGroups.Items {
// 		units, err := GetUnitsByNodeGroup(kubeClient, siteClient, &nodeGroup)
// 		if err != nil {
// 			klog.Errorf("Get NodeGroup unit error: %v", err)
// 			continue
// 		}

// 		for _, unit := range units {
// 			if unit == unitName {
// 				nodeGroups = append(nodeGroups, nodeGroup)
// 			}
// 		}
// 	}

// 	return nodeGroups, nil
// }

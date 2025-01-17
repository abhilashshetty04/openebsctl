/*
Copyright 2020-2022 The OpenEBS Authors

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

package volume

import (
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/cli-runtime/pkg/printers"

	"github.com/openebs/openebsctl/pkg/client"
	"github.com/openebs/openebsctl/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// JivaVolInfoTemplate to store the jiva volume and pvc describe related details
	JivaVolInfoTemplate = `
{{.Name}} Details :
-----------------
NAME            : {{.Name}}
ACCESS MODE     : {{.AccessMode}}
CSI DRIVER      : {{.CSIDriver}}
STORAGE CLASS   : {{.StorageClass}}
VOLUME PHASE    : {{.VolumePhase }}
VERSION         : {{.Version}}
JVP             : {{.JVP}}
SIZE            : {{.Size}}
STATUS          : {{.Status}}
REPLICA COUNT	: {{.ReplicaCount}}

`
	// JivaPortalTemplate to store the portal details for jiva volume and pvc
	JivaPortalTemplate = `
Portal Details :
------------------
IQN              :  {{.spec.iscsiSpec.iqn}}
VOLUME NAME      :  {{.metadata.name}}
TARGET NODE NAME :  {{.metadata.labels.nodeID}}
PORTAL           :  {{.spec.iscsiSpec.targetIP}}:{{.spec.iscsiSpec.targetPort}}

`
)

// GetJiva returns a list of JivaVolumes
func GetJiva(c *client.K8sClient, pvList *corev1.PersistentVolumeList, openebsNS string) ([]metav1.TableRow, error) {
	// 1. Fetch all relevant volume CRs without worrying about openebsNS
	_, jvMap, err := c.GetJVs(nil, util.Map, "", util.MapOptions{Key: util.Name})
	if err != nil {
		return nil, fmt.Errorf("failed to list JivaVolumes")
	}
	var rows []metav1.TableRow
	// 3. Show the required ones
	for _, pv := range pvList.Items {
		name := pv.Name
		capacity := pv.Spec.Capacity.Storage()
		sc := pv.Spec.StorageClassName
		attached := pv.Status.Phase
		var attachedNode, storageVersion, customStatus, ns string
		// Should all AccessModes be shown in a csv format, or the highest be displayed ROO < RWO < RWX?
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == util.JivaCSIDriver {
			jv, ok := jvMap[pv.Name]
			if !ok {
				_, _ = fmt.Fprintln(os.Stderr, "couldn't find jv "+pv.Name)
			}
			ns = jv.Namespace
			if openebsNS != "" && openebsNS != ns {
				continue
			}
			customStatus = jv.Status.Status // RW, RO, etc
			attachedNode = jv.Labels["nodeID"]
			storageVersion = jv.VersionDetails.Status.Current
		} else {
			// Skip non-Jiva options
			continue
		}
		accessMode := pv.Spec.AccessModes[0]
		rows = append(rows, metav1.TableRow{
			Cells: []interface{}{
				ns, name, customStatus, storageVersion, capacity, sc, attached,
				accessMode, attachedNode},
		})
	}
	return rows, nil
}

// DescribeJivaVolume describes a jiva storage engine PersistentVolume
func DescribeJivaVolume(c *client.K8sClient, vol *corev1.PersistentVolume) error {
	// 1. Get the JivaVolume Corresponding to the pv name
	jv, err := c.GetJV(vol.Name)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to get JivaVolume for %s\n", vol.Name)
		return err
	}
	// 2. Fill in JivaVolume related details
	jivaVolInfo := util.VolumeInfo{
		AccessMode:   util.AccessModeToString(vol.Spec.AccessModes),
		Capacity:     util.ConvertToIBytes(vol.Spec.Capacity.Storage().String()),
		CSIDriver:    vol.Spec.CSI.Driver,
		Name:         jv.Name,
		Namespace:    jv.Namespace,
		PVC:          vol.Spec.ClaimRef.Name,
		ReplicaCount: jv.Spec.Policy.Target.ReplicationFactor,
		VolumePhase:  vol.Status.Phase,
		StorageClass: vol.Spec.StorageClassName,
		Version:      jv.VersionDetails.Status.Current,
		Size:         util.ConvertToIBytes(vol.Spec.Capacity.Storage().String()),
		Status:       jv.Status.Status,
		JVP:          jv.Annotations["openebs.io/volume-policy"],
	}
	// 3. Print the Volume information
	_ = util.PrintByTemplate("jivaVolumeInfo", JivaVolInfoTemplate, jivaVolInfo)
	// 4. Print the Portal Information
	util.TemplatePrinter(JivaPortalTemplate, jv)

	replicaPodIPAndModeMap := make(map[string]string)
	// Create Replica IP to Mode Map
	if jv.Status.ReplicaStatuses != nil && len(jv.Status.ReplicaStatuses) != 0 {
		for _, replicaStatus := range jv.Status.ReplicaStatuses {
			replicaPodIPAndModeMap[strings.Split(replicaStatus.Address, ":")[1][2:]] = replicaStatus.Mode
		}
	}

	// 5. Fetch the Jiva controller and replica pod details
	podList, err := c.GetJVTargetPod(vol.Name)
	if err == nil {
		fmt.Println("Controller and Replica Pod Details :")
		fmt.Println("-----------------------------------")
		var rows []metav1.TableRow
		for _, pod := range podList.Items {
			if strings.Contains(pod.Name, "-ctrl-") {
				rows = append(rows, metav1.TableRow{Cells: []interface{}{
					pod.Namespace, pod.Name, jv.Status.Status,
					pod.Spec.NodeName, pod.Status.Phase, pod.Status.PodIP,
					util.GetReadyContainers(pod.Status.ContainerStatuses),
					util.Duration(time.Since(pod.ObjectMeta.CreationTimestamp.Time))}})
			} else {
				if val, ok := replicaPodIPAndModeMap[pod.Status.PodIP]; ok {
					rows = append(rows, metav1.TableRow{Cells: []interface{}{
						pod.Namespace, pod.Name, val,
						pod.Spec.NodeName, pod.Status.Phase, pod.Status.PodIP,
						util.GetReadyContainers(pod.Status.ContainerStatuses),
						util.Duration(time.Since(pod.ObjectMeta.CreationTimestamp.Time))}})
				}
			}
		}
		util.TablePrinter(util.JivaPodDetailsColumnDefinations, rows, printers.PrintOptions{Wide: true})
	} else {
		fmt.Println("Controller and Replica Pod Details :")
		fmt.Println("-----------------------------------")
		fmt.Println("No Controller and Replica pod exists for the JivaVolume")
	}
	// 6. Fetch the replica PVCs and create rows for cli-runtime
	var rows []metav1.TableRow
	pvcList, err := c.GetPVCs(c.Ns, nil, "openebs.io/component=jiva-replica,openebs.io/persistent-volume="+jv.Name)
	if err != nil || len(pvcList.Items) == 0 {
		fmt.Printf("No replicas found for the JivaVolume %s", vol.Name)
		return nil
	}
	for _, pvc := range pvcList.Items {
		rows = append(rows, metav1.TableRow{Cells: []interface{}{
			pvc.Name,
			pvc.Status.Phase,
			pvc.Spec.VolumeName,
			util.ConvertToIBytes(pvc.Spec.Resources.Requests.Storage().String()),
			*pvc.Spec.StorageClassName,
			util.Duration(time.Since(pvc.ObjectMeta.CreationTimestamp.Time)),
			pvc.Spec.VolumeMode}})
	}
	// 6. Print the replica details if present
	fmt.Println()
	fmt.Println("Replica Data Volume Details :")
	fmt.Println("-----------------------------")
	util.TablePrinter(util.JivaReplicaPVCColumnDefinations, rows, printers.PrintOptions{Wide: true})
	return nil
}

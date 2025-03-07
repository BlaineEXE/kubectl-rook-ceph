/*
Copyright 2023 The Rook Authors. All rights reserved.

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

package debug

import (
	"context"
	"fmt"
	"strings"

	"github.com/rook/kubectl-rook-ceph/pkg/logging"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func StopDebug(ctx context.Context, k8sclientset kubernetes.Interface, clusterNamespace, deploymentName string) {

	err := stopDebug(ctx, k8sclientset, clusterNamespace, deploymentName)
	if err != nil {
		logging.Fatal(err)
	}
}

func stopDebug(ctx context.Context, k8sclientset kubernetes.Interface, clusterNamespace, deploymentName string) error {
	if !strings.HasSuffix(deploymentName, "-debug") {
		deploymentName = deploymentName + "-debug"
	}

	debugDeployment, err := GetDeployment(ctx, k8sclientset, clusterNamespace, deploymentName)
	if err != nil {
		return fmt.Errorf("Missing mon or osd debug deployment name %s. %v\n", deploymentName, err)
	}

	logging.Info("removing debug mode from deployment %s\n", debugDeployment.Name)
	err = k8sclientset.AppsV1().Deployments(clusterNamespace).Delete(ctx, debugDeployment.Name, v1.DeleteOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("Error deleting deployment %s: %v", debugDeployment.Name, err)
	}

	original_deployment_name := strings.ReplaceAll(deploymentName, "-debug", "")
	if err := SetDeploymentScale(ctx, k8sclientset, clusterNamespace, original_deployment_name, 1); err != nil {
		return err
	}
	logging.Info("Successfully deleted debug deployment and restored deployment %q", original_deployment_name)
	return nil
}

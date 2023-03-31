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

package multus

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"text/template"
	"time"

	"gopkg.in/yaml.v2"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// the name of the config object that "owns" all other resources created for this test
	// this does not need to be unique per test. per-namespace is fine
	ownerConfigMapName = "multus-validation-test-config"

	// the config object key that contains config for the validation test
	ownerConfigMapConfigKey = "config"
)

var (
	//go:embed nginx-deploy.yaml
	NginxDeploymentTemplate string

	//go:embed nginx-config.yaml
	NginxConfigTemplate string
)

type validationTest struct {
	clientset            kubernetes.Clientset
	clusterNamespace     string
	multusPublicNetwork  string
	multusClusterNetwork string
}

func NewValidationTest(
	clientset kubernetes.Clientset,
	clusterNamespace string,
	multusPublicNetwork string,
	multusClusterNetwork string,
) *validationTest {
	vt := &validationTest{
		clientset:            clientset,
		clusterNamespace:     clusterNamespace,
		multusPublicNetwork:  multusPublicNetwork,
		multusClusterNetwork: multusClusterNetwork,
	}
	return vt
}

func (vt *validationTest) Run(ctx context.Context) error {
	fmt.Println("starting multus validation test with the following config:")
	fmt.Println("namespace:", vt.clusterNamespace)
	fmt.Println("multus public network:", vt.multusPublicNetwork)

	defer vt.cleanupTestResources()

	if err := vt.startTestResources(ctx); err != nil {
		return fmt.Errorf("failed to start multus validation test")
	}

	// TODO: check status of all pods

	time.Sleep(30 * time.Second)

	return nil
}

func (vt *validationTest) startTestResources(ctx context.Context) error {
	testConfigAsOwner, err := vt.createTestConfigObject(ctx)
	if err != nil {
		return fmt.Errorf("failed to create validation test config object. %w", err)
	}

	err = vt.startWebServer(ctx, testConfigAsOwner)
	if err != nil {
		return fmt.Errorf("failed to start web server. %w", err)
	}

	return nil
}

func (vt *validationTest) cleanupTestResources() {
	// need a clean, non-canceled context in case the test is canceled by ctrl-c
	ctx := context.Background()

	fmt.Println("please wait for multus validation test resources to be cleaned up, or "+
		"manually delete owner configmap %q", ownerConfigMapName)

	// delete the config object in the foreground so we wait until all validation test resources are
	// gone before stopping
	deleteForeground := meta.DeletePropagationForeground
	delOpts := meta.DeleteOptions{
		PropagationPolicy: &deleteForeground,
	}
	err := vt.clientset.CoreV1().ConfigMaps(vt.clusterNamespace).Delete(ctx, ownerConfigMapName, delOpts)
	if err != nil {
		if !errors.IsNotFound(err) {
			fmt.Println("failed to clean up multus validation test resources"+
				"please manually delete owner configmap %q to perform cleanup", ownerConfigMapName)
		}
	}
}

// create a validation test config object that stores the configuration of the running validation
// test. this object serves as the owner of all associated test objects. when this object is
// deleted, all validation test objects should also be deleted, effectively cleaning up all
// components of this test.
func (vt *validationTest) createTestConfigObject(ctx context.Context) ([]meta.OwnerReference, error) {
	testConfig, err := json.Marshal(*vt)
	if err != nil {
		return nil, fmt.Errorf("failed to render validation test config [%+v] to a string: %w", *vt, err)
	}

	isImmutable := true // don't let users modify the test config after it starts
	c := core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Name: ownerConfigMapName,
		},
		Immutable: &isImmutable,
		Data: map[string]string{
			ownerConfigMapConfigKey: string(testConfig),
		},
	}

	configObject, err := vt.clientset.CoreV1().ConfigMaps(vt.clusterNamespace).Create(ctx, c, meta.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create validation test config object [%+v]: %w", c, err)
	}

	// for cleanup, we want to make sure all children are deleted
	BlockOwnerDeletion := true
	refToConfigObject := v1.OwnerReference{
		APIVersion:         configObject.APIVersion,
		Kind:               configObject.Kind,
		Name:               configObject.GetName(),
		UID:                configObject.GetUID(),
		BlockOwnerDeletion: &BlockOwnerDeletion,
	}
	return []meta.OwnerReference{refToConfigObject}, nil
}

func (vt *validationTest) startWebServer(ctx context.Context, owners []meta.OwnerReference) error {
	t, err := loadTemplate("webServerDeployment", NginxDeploymentTemplate, vt)
	if err != nil {
		return fmt.Errorf("failed to load deployment template: %w", err)
	}
	var dep *apps.Deployment
	err = yaml.Unmarshal(t, &dep)
	if err != nil {
		return fmt.Errorf("failed to unmarshal deployment template: %w", err)
	}

	t, err = loadTemplate("configMapTemplate", NginxConfigTemplate, vt)
	if err != nil {
		return fmt.Errorf("failed to load configmap template: %w", err)
	}
	var configMap *core.ConfigMap
	err = yaml.Unmarshal(t, &configMap)
	if err != nil {
		return fmt.Errorf("failed to unmarshal configmap template: %w", err)
	}

	configMap.SetOwnerReferences(owners)
	_, err = vt.clientset.CoreV1().ConfigMaps(vt.clusterNamespace).Create(ctx, configMap, v1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create web server config: %w", err)
	}

	dep.SetOwnerReferences(owners)
	_, err = vt.clientset.AppsV1().Deployments(vt.clusterNamespace).Create(ctx, dep, v1.CreateOptions{})
	if err != nil {
		if err != nil {
			return fmt.Errorf("failed to create web server: %w", err)
		}
	}

	return nil
}

func loadTemplate(name, templateFileText string, vt *validationTest) ([]byte, error) {
	var writer bytes.Buffer
	t := template.New(name)
	t, err := t.Parse(templateFileText)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %q: %w", name, err)
	}
	err = t.Execute(&writer, vt)
	return writer.Bytes(), err
}

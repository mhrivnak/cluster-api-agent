package imageRegistry

import (
	"context"
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/pelletier/go-toml"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	registryConfKey                = "registries.conf"
	registryCertKey                = "ca-bundle.crt"
	imageRegistryName              = "additional-registry"
	registryCertConfigMapName      = "additional-registry-certificate"
	registryCertConfigMapNamespace = "openshift-config"
	imageConfigMapName             = "additional-registry-config"
	imageDigestMirrorSetKey        = "image-digest-mirror-set.json"
	registryCertConfigMapKey       = "additional-registry-certificate.json"
	imageConfigKey                 = "image-config.json"
)

// CreateConfig creates a ConfigMap containing the manifests to be created on the spoke cluster.
// The manifests that are in the ConfigMap include a ImageDigestMirrorSet CR with the mirror registry information,
// a ConfigMap containing the additional trusted certificates for the mirror registry, and an image.config.openshift.io
// CR that references the ConfigMap with the additional trusted certificate.
// Successful creation of the ConfigMap will return the name of the created ConfigMap to be added as a manifest reference
func CreateConfig(ctx context.Context, client client.Client, registryRef *corev1.LocalObjectReference, namespace string) (string, error) {

	imageRegistryConfigMap := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Name: registryRef.Name, Namespace: namespace}, imageRegistryConfigMap); err != nil {
		return "", err
	}

	additionalRegistryConfigMap, err := createImageRegistryConfig(imageRegistryConfigMap, namespace)
	if err != nil {
		return "", err
	}

	if err := client.Create(ctx, additionalRegistryConfigMap); err != nil {
		return "", err
	}
	return additionalRegistryConfigMap.Name, nil
}

func createImageRegistryConfig(imageRegistry *corev1.ConfigMap, namespace string) (*corev1.ConfigMap, error) {
	imageConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageConfigMapName,
			Namespace: namespace,
		},
		Data: make(map[string]string),
	}
	registryConf, ok := imageRegistry.Data[registryConfKey]
	if ok {
		imageDigestMirrorSet, err := getImageRegistries(registryConf)
		if err != nil {
			return nil, err
		}
		imageConfig.Data[imageDigestMirrorSetKey] = imageDigestMirrorSet
	}

	registryCert, ok := imageRegistry.Data[registryCertKey]
	if ok {
		addImageRegistryCertificate(imageConfig, registryCert)
	}
	return imageConfig, nil
}

// getImageRegistries reads a toml tree string with the structure:
//
// [[registry]]
//
//	location = "source-registry"
//
//	 [[registry.mirror]]
//	 location = "mirror-registry"
//
// and converts it to an ImageDigestMirrorSet CR
// and returns it as a marshalled JSON string
func getImageRegistries(registry string) (string, error) {
	// Parse toml and add mirror registry to image digest mirror set
	tomlTree, err := toml.Load(registry)
	if err != nil {
		return "", err
	}

	idmsMirrors := make([]configv1.ImageDigestMirrors, 0)
	registriesTree, ok := tomlTree.Get("registry").([]*toml.Tree)
	if !ok {
		return "", fmt.Errorf("failed to find registry in toml tree")
	}

	// Add each registry and its mirrors as a image digest mirror object
	for _, registryTree := range registriesTree {
		var imageDigestMirror configv1.ImageDigestMirrors
		source, ok := registryTree.Get("location").(string)
		if !ok {
			continue
		}
		imageDigestMirror.Source = source

		var mirrors []configv1.ImageMirror
		mirrorTrees, ok := registryTree.Get("mirror").([]*toml.Tree)
		if !ok {
			continue
		}

		for _, mirrorTree := range mirrorTrees {
			mirror, ok := mirrorTree.Get("location").(string)
			if ok {
				mirrors = append(mirrors, configv1.ImageMirror(mirror))
			}
		}

		if len(mirrors) < 1 {
			continue
		}
		imageDigestMirror.Mirrors = mirrors
		idmsMirrors = append(idmsMirrors, imageDigestMirror)
	}

	if len(idmsMirrors) < 1 {
		return "", fmt.Errorf("failed to find any mirror registry")
	}

	idms := configv1.ImageDigestMirrorSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageDigestMirrorSet",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: imageRegistryName,
		},
		Spec: configv1.ImageDigestMirrorSetSpec{
			ImageDigestMirrors: idmsMirrors,
		},
	}

	idmsJSON, err := json.Marshal(idms)
	if err != nil {
		return "", err
	}

	return string(idmsJSON), nil
}

// addImageRegistryCertificate creates a ConfigMap CR containing the registry certificate to be created on the spoke cluster
// and creates a image.config.openshift.io CR that references that ConfigMap CR. It marshalls these two resources
// and adds them to a ConfigMap that will be used as an additional manifest for the spoke cluster.
func addImageRegistryCertificate(configMap *corev1.ConfigMap, certificate string) {
	// Add certificate as a ConfigMap manifest to be created in the spoke cluster's openshift-config namespace
	certificateCM := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      registryCertConfigMapName,
			Namespace: registryCertConfigMapNamespace,
		},
		Data: map[string]string{
			registryCertKey: certificate,
		},
	}

	certJSON, err := json.Marshal(certificateCM)
	if err != nil {
		return
	}
	configMap.Data[registryCertConfigMapKey] = string(certJSON)

	// Add certificate to image.config.openshift.io as additional trusted CA
	clusterImageConfig := &configv1.Image{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Image",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.ImageSpec{
			AdditionalTrustedCA: configv1.ConfigMapNameReference{
				Name: registryCertConfigMapName,
			},
		},
	}

	clusterImageJSON, err := json.Marshal(clusterImageConfig)
	if err != nil {
		return
	}
	configMap.Data[imageConfigKey] = string(clusterImageJSON)
}

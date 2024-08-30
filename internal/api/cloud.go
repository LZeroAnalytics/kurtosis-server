// Package api cloud.go
package api

import (
	"bytes"
	"context"
	"fmt"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"kurtosis-server/internal/api/util"
	"log"
	"text/template"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

type Port struct {
	PortName string
	Port     int32
}

type IngressData struct {
	ServiceName string
	SessionID   string
	Namespace   string
	Ports       []Port
}

func loadTemplate(templatePath string) (*template.Template, error) {
	content, err := ioutil.ReadFile(templatePath)
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("ingress").Parse(string(content))
	if err != nil {
		return nil, err
	}

	return tmpl, nil
}

// Helper function to create an ingress using a given template path
func createIngressFromTemplate(data IngressData, templatePath string) error {
	fmt.Printf("Creating ingress with the following data: %v\n", data)
	config, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	if err != nil {
		return err
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	fmt.Printf("Created dynamic client\n")
	fmt.Printf("Trying to reduce Ports: %v\n", data.Ports)

	// Serve on root path if only one port and it's not gRPC
	if len(data.Ports) == 1 && data.Ports[0].PortName != "grpc" {
		data.Ports[0].PortName = ""
	}

	fmt.Printf("Reduced ports: %v\n", data.Ports)

	// Load the service template
	tmpl, err := loadTemplate(templatePath)
	if err != nil {
		return err
	}

	fmt.Printf("Loaded template\n")

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Print the rendered template for debugging
	fmt.Println("Rendered ingress YAML:")
	fmt.Println(buf.String())

	// Convert the rendered template to an unstructured object
	ingress := &unstructured.Unstructured{}
	dec := runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), buf.Bytes(), ingress)
	if dec != nil {
		return err
	}

	fmt.Printf("Creating the following ingress: %v\n", ingress)

	resource := schema.GroupVersionResource{
		Group:    "networking.k8s.io",
		Version:  "v1",
		Resource: "ingresses",
	}

	namespace := data.Namespace
	name := ingress.GetName()

	// Check if the service already exists
	_, err = dynClient.Resource(resource).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Service does not exist, create it
			_, err = dynClient.Resource(resource).Namespace(namespace).Create(context.Background(), ingress, metav1.CreateOptions{})
			if err != nil {
				fmt.Printf("Error creating service: %v\n", err)
				return err
			}
		} else {
			fmt.Printf("Service exists or other error: %v\n", err)
			return err
		}
	}

	return nil
}

// Original createIngress function, now uses the helper function
func createIngress(data IngressData) error {
	templatePath := "/home/ubuntu/kurtosis-server/internal/api/templates/ingress.tmpl"
	return createIngressFromTemplate(data, templatePath)
}

// New function to create a gRPC-specific ingress
func createGrpcIngress(data IngressData) error {
	templatePath := "/home/ubuntu/kurtosis-server/internal/api/templates/grpc-ingress.tmpl"
	return createIngressFromTemplate(data, templatePath)
}

// Updated function to handle services with both gRPC and non-gRPC ports
func createIngresses(bgContext context.Context, kurtosisCtx *kurtosis_context.KurtosisContext, runPackageMessage RunPackageMessage, sessionID string, enclaveName string) {
	for _, serviceMapping := range runPackageMessage.ServiceMappings {
		ingressData := IngressData{
			ServiceName: serviceMapping.ServiceName,
			SessionID:   sessionID[:18],
			Namespace:   "kt-" + enclaveName,
			Ports:       serviceMapping.Ports,
		}

		// Extract gRPC port and non-gRPC ports
		var grpcPorts []Port
		var nonGrpcPorts []Port
		for _, port := range ingressData.Ports {
			if port.PortName == "grpc" {
				grpcPorts = append(grpcPorts, port)
			} else {
				nonGrpcPorts = append(nonGrpcPorts, port)
			}
		}

		// Create gRPC ingress if there is a gRPC port
		if len(grpcPorts) > 0 {
			grpcIngressData := IngressData{
				ServiceName: ingressData.ServiceName,
				SessionID:   ingressData.SessionID,
				Namespace:   ingressData.Namespace,
				Ports:       grpcPorts,
			}
			err := createGrpcIngress(grpcIngressData)
			if err != nil {
				handleIngressError(bgContext, kurtosisCtx, err, enclaveName, serviceMapping.ServiceName)
				continue
			}
		}

		// Create standard ingress for non-gRPC ports
		if len(nonGrpcPorts) > 0 {
			nonGrpcIngressData := IngressData{
				ServiceName: ingressData.ServiceName,
				SessionID:   ingressData.SessionID,
				Namespace:   ingressData.Namespace,
				Ports:       nonGrpcPorts,
			}
			err := createIngress(nonGrpcIngressData)
			if err != nil {
				handleIngressError(bgContext, kurtosisCtx, err, enclaveName, serviceMapping.ServiceName)
			}
		}
	}
}

// Handle ingress creation errors
func handleIngressError(bgContext context.Context, kurtosisCtx *kurtosis_context.KurtosisContext, err error, enclaveName, serviceName string) {
	deletionDate := time.Now().Format(time.RFC3339)
	util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
	kurtosisCtx.DestroyEnclave(bgContext, enclaveName)
	log.Printf("Failed to create ingress for service %s: %v", serviceName, err)
}

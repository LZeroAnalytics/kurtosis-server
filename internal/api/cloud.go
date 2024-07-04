// cloud.go
package api

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"strings"
	"text/template"

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

type ServiceData struct {
	ServiceName string
	HostName    string
	Namespace   string
	Domain      string
	Ports       []Port
}

func loadTemplate(templatePath string) (*template.Template, error) {
	content, err := ioutil.ReadFile(templatePath)
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("service").Parse(string(content))
	if err != nil {
		return nil, err
	}

	return tmpl, nil
}

func createService(data ServiceData) error {

	fmt.Printf("Creating service with the following data: %v", data)
	config, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	if err != nil {
		return err
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	// Load the service template
	templatePath := "/home/ubuntu/kurtosis-server/internal/api/templates/service.tmpl"
	tmpl, err := loadTemplate(templatePath)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Print the rendered template for debugging
	fmt.Println("Rendered service YAML:")
	fmt.Println(buf.String())

	// Convert the rendered template to an unstructured object
	service := &unstructured.Unstructured{}
	dec := runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), buf.Bytes(), service)
	if dec != nil {
		return err
	}

	fmt.Printf("Creating the following service: %v", service)

	resource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "services",
	}

	namespace := data.Namespace
	name := service.GetName()

	// Check if the service already exists
	_, err = dynClient.Resource(resource).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Service does not exist, create it
			_, err = dynClient.Resource(resource).Namespace(namespace).Create(context.Background(), service, metav1.CreateOptions{})
			if err != nil {
				fmt.Printf("Error creating service: %v:", err)
				return err
			}
		} else {
			fmt.Printf("Service exists or other error: %v", err)
			return err
		}
	}

	return nil
}

func deleteServices(namespace string) error {
	config, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	if err != nil {
		return err
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	resource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "services",
	}

	// List all services in the namespace
	servicesList, err := dynClient.Resource(resource).Namespace(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	// Delete each load balancer service
	for _, service := range servicesList.Items {
		if strings.HasSuffix(service.GetName(), "-lb") {
			err := dynClient.Resource(resource).Namespace(namespace).Delete(context.Background(), service.GetName(), metav1.DeleteOptions{})
			if err != nil {
				return err
			}
			fmt.Printf("Deleted service: %s\n", service.GetName())
		}
	}

	return nil
}

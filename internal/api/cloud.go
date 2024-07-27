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

func createIngress(data IngressData) error {

	fmt.Printf("Creating service with the following data: %v", data)
	config, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	if err != nil {
		return err
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	fmt.Printf("Created dynmao db client")
	fmt.Printf("Trying to reduce Ports: %v", data.Ports)

	// Serve on root path if only one port
	if len(data.Ports) == 1 {
		data.Ports[0].PortName = ""
	}

	fmt.Printf("Reduced ports: %v", data.Ports)

	// Load the service template
	templatePath := "/home/ubuntu/kurtosis-server/internal/api/templates/ingress.tmpl"
	tmpl, err := loadTemplate(templatePath)
	if err != nil {
		return err
	}

	fmt.Printf("Loaded template")

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Print the rendered template for debugging
	fmt.Println("Rendered service YAML:")
	fmt.Println(buf.String())

	// Convert the rendered template to an unstructured object
	ingress := &unstructured.Unstructured{}
	dec := runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), buf.Bytes(), ingress)
	if dec != nil {
		return err
	}

	fmt.Printf("Creating the following ingress: %v", ingress)

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

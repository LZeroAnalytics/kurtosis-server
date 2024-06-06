package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"net/http"
	"path/filepath"
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
	Namespace   string
	Domain      string
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
	config, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	if err != nil {
		return err
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	// Load the ingress template
	templatePath := filepath.Join("templates", "ingress.tmpl")
	tmpl, err := loadTemplate(templatePath)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	// Convert the rendered template to an unstructured object
	ingress := &unstructured.Unstructured{}
	dec := runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), buf.Bytes(), ingress)
	if dec != nil {
		return err
	}

	_, err = dynClient.Resource(schema.GroupVersionResource{
		Group:    "networking.k8s.io",
		Version:  "v1",
		Resource: "ingresses",
	}).Namespace(data.Namespace).Create(context.Background(), ingress, metav1.CreateOptions{})

	return err
}

func GetServiceURLs(w http.ResponseWriter, r *http.Request) {
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	if enclaveIdentifier == "" {
		http.Error(w, "Missing enclaveIdentifier query parameter", http.StatusBadRequest)
		return
	}

	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	enclaveCtx, err := kurtosisCtx.GetEnclaveContext(context.Background(), enclaveIdentifier)
	if err != nil {
		http.Error(w, "Failed to get EnclaveContext: "+err.Error(), http.StatusInternalServerError)
		return
	}

	serviceIdentifiers, err := enclaveCtx.GetServices()
	if err != nil {
		http.Error(w, "Failed to get services: "+err.Error(), http.StatusInternalServerError)
		return
	}

	serviceIdentifiersMap := make(map[string]bool)
	for serviceName := range serviceIdentifiers {
		serviceIdentifiersMap[string(serviceName)] = true
	}

	serviceContexts, err := enclaveCtx.GetServiceContexts(serviceIdentifiersMap)
	if err != nil {
		http.Error(w, "Failed to get service contexts: "+err.Error(), http.StatusInternalServerError)
		return
	}

	servicesInfo := make(map[string]interface{})
	for _, serviceContext := range serviceContexts {
		serviceName := string(serviceContext.GetServiceName())
		ports := []Port{}
		for portName, port := range serviceContext.GetPrivatePorts() {
			ports = append(ports, Port{PortName: portName, Port: int32(port.GetNumber())})
		}

		ingressData := IngressData{
			ServiceName: serviceName,
			Namespace:   "kt-" + enclaveIdentifier,
			Domain:      "lzeroanalytics.com",
			Ports:       ports,
		}

		if err := createIngress(ingressData); err != nil {
			http.Error(w, "Failed to create ingress: "+err.Error(), http.StatusInternalServerError)
			return
		}

		servicesInfo[serviceName] = map[string]interface{}{
			"urls": createServiceURLs(ingressData),
		}
	}

	servicesInfoJSON, err := json.Marshal(servicesInfo)
	if err != nil {
		http.Error(w, "Failed to serialize services information: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Write(servicesInfoJSON)
}

func createServiceURLs(data IngressData) []string {
	urls := []string{}
	for _, port := range data.Ports {
		url := fmt.Sprintf("http://%s.%s/%s", data.ServiceName, data.Domain, port.PortName)
		urls = append(urls, url)
	}
	return urls
}

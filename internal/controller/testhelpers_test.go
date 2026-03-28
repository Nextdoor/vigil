package controller_test

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

var testScheme *runtime.Scheme

func init() {
	testScheme = runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
}

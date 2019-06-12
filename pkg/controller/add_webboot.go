package controller

import (
	"github.com/logancloud/logan-app-operator/pkg/controller/webboot"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, webboot.Add)
}
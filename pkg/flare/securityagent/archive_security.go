// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package securityagent contains flare logic for the security agent
package securityagent

import (
	"os"

	flarehelpers "github.com/DataDog/datadog-agent/comp/core/flare/helpers"
	flaretypes "github.com/DataDog/datadog-agent/comp/core/flare/types"
	"github.com/DataDog/datadog-agent/comp/core/status"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/flare/common"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// CreateSecurityAgentArchive packages up the files
func CreateSecurityAgentArchive(local bool, logFilePath string, statusComponent status.Component) (string, error) {
	fb, err := flarehelpers.NewFlareBuilder(local, flaretypes.FlareArgs{})
	if err != nil {
		return "", err
	}
	createSecurityAgentArchive(fb, logFilePath, statusComponent)

	return fb.Save()
}

// createSecurityAgentArchive packages up the files
func createSecurityAgentArchive(fb flaretypes.FlareBuilder, logFilePath string, statusComponent status.Component) {
	// If the request against the API does not go through we don't collect the status log.
	if fb.IsLocal() {
		fb.AddFile("local", []byte("")) //nolint:errcheck
	} else {
		// The Status will be unavailable unless the agent is running.
		// Only zip it up if the agent is running
		err := fb.AddFileFromFunc("security-agent-status.log", func() ([]byte, error) {
			return statusComponent.GetStatus("text", false)
		})
		if err != nil {
			log.Infof("Error getting the status of the Security Agent, %q", err)
			return
		}
	}

	common.GetLogFiles(fb, logFilePath)
	common.GetConfigFiles(fb, map[string]string{})
	getComplianceFiles(fb)                               //nolint:errcheck
	getRuntimeFiles(fb)                                  //nolint:errcheck
	common.GetExpVar(fb)                                 //nolint:errcheck
	fb.AddFileFromFunc("envvars.log", common.GetEnvVars) //nolint:errcheck

	addSecurityAgentPlatformSpecificEntries(fb)
}

func getComplianceFiles(fb flaretypes.FlareBuilder) error {
	compDir := pkgconfigsetup.Datadog().GetString("compliance_config.dir")

	return fb.CopyDirTo(compDir, "compliance.d", func(path string) bool {
		f, err := os.Lstat(path)
		if err != nil {
			return false
		}
		return f.Mode()&os.ModeSymlink == 0
	})
}

func getRuntimeFiles(fb flaretypes.FlareBuilder) error {
	runtimeDir := pkgconfigsetup.SystemProbe().GetString("runtime_security_config.policies.dir")

	return fb.CopyDirTo(runtimeDir, "runtime-security.d", func(path string) bool {
		f, err := os.Lstat(path)
		if err != nil {
			return false
		}
		return f.Mode()&os.ModeSymlink == 0
	})
}

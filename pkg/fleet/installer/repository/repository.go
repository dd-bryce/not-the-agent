// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package repository contains the packaging logic of the updater.
package repository

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/DataDog/datadog-agent/pkg/fleet/installer/paths"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	stableVersionLink     = "stable"
	experimentVersionLink = "experiment"
)

var (
	errRepositoryNotCreated = errors.New("repository not created")
)

// PreRemoveHook are called before a package is removed.  It returns a boolean
// indicating if the package files can be deleted safely and an error if an error happened
// when running the hook.
type PreRemoveHook func(context.Context, string) (bool, error)

// Repository contains the stable and experimental package of a single artifact managed by the updater.
//
// On disk the repository is structured as follows:
// .
// ├── 7.50.0
// ├── 7.51.0
// ├── stable -> 7.50.0 (symlink)
// └── experiment -> 7.51.0 (symlink)
//
// We voluntarily do not load the state of the repository in memory to avoid any bugs where
// what's on disk and what's in memory are not in sync.
// All the functions of the repository are "atomic" and ensure no invalid state can be reached
// even if an error happens during their execution.
// It is possible to end up with garbage left on disk if an error happens during some operations. This
// is cleaned up during the next operation.
type Repository struct {
	rootPath       string
	preRemoveHooks map[string]PreRemoveHook
}

// PackageStates contains the state all installed packages
type PackageStates struct {
	States       map[string]State `json:"states"`
	ConfigStates map[string]State `json:"config_states"`
}

// State is the state of the repository.
type State struct {
	Stable     string `json:"stable"`
	Experiment string `json:"experiment"`
}

// HasStable returns true if the repository has a stable package.
func (s *State) HasStable() bool {
	return s.Stable != ""
}

// HasExperiment returns true if the repository has an experiment package.
func (s *State) HasExperiment() bool {
	return s.Experiment != ""
}

// StableFS returns the stable package fs.
func (r *Repository) StableFS() fs.FS {
	return os.DirFS(filepath.Join(r.rootPath, stableVersionLink))
}

// ExperimentFS returns the experiment package fs.
func (r *Repository) ExperimentFS() fs.FS {
	return os.DirFS(filepath.Join(r.rootPath, experimentVersionLink))
}

// GetState returns the state of the repository.
func (r *Repository) GetState() (State, error) {
	repository, err := readRepository(r.rootPath, r.preRemoveHooks)
	if errors.Is(err, errRepositoryNotCreated) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	stable := repository.stable.Target()
	experiment := repository.experiment.Target()
	if experiment == stable {
		experiment = ""
	}
	return State{
		Stable:     stable,
		Experiment: experiment,
	}, nil
}

// Create creates a fresh new repository at the given root path
// and moves the given stable source path to the repository as the first stable.
// If a repository already exists at the given path, it is fully removed.
//
// 1. Remove the previous repository if it exists.
// 2. Create the root directory.
// 3. Move the stable source to the repository.
// 4. Create the stable link.
func (r *Repository) Create(ctx context.Context, name string, stableSourcePath string) error {
	err := os.MkdirAll(r.rootPath, 0755)
	if err != nil {
		return fmt.Errorf("could not create packages root directory: %w", err)
	}

	repository, err := readRepository(r.rootPath, r.preRemoveHooks)
	if err != nil {
		return err
	}

	// Remove symlinks as we are (re)-installing the package
	if repository.experiment.Exists() {
		err = repository.experiment.Delete()
		if err != nil {
			return fmt.Errorf("could not delete experiment link: %w", err)
		}
	}
	if repository.stable.Exists() {
		err = repository.stable.Delete()
		if err != nil {
			return fmt.Errorf("could not delete stable link: %w", err)
		}
	}

	err = repository.cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository: %w", err)
	}

	err = repository.setStable(name, stableSourcePath)
	if err != nil {
		return fmt.Errorf("could not set first stable: %w", err)
	}
	err = repository.setExperimentToStable()
	if err != nil {
		return fmt.Errorf("could not set first experiment: %w", err)
	}
	return nil
}

// Delete deletes the repository.
//
// 1. Remove the stable and experiment links.
// 2. Cleanup the repository to remove all package versions after running the pre-remove hooks.
// 3. Remove the root directory.
func (r *Repository) Delete(ctx context.Context) error {
	// Remove symlinks first so that cleanup will attempt to remove all package versions
	repositoryFiles, err := readRepository(r.rootPath, r.preRemoveHooks)
	if err != nil {
		return err
	}
	if repositoryFiles.experiment.Exists() {
		err = repositoryFiles.experiment.Delete()
		if err != nil {
			return fmt.Errorf("could not delete experiment link: %w", err)
		}
	}
	if repositoryFiles.stable.Exists() {
		err = repositoryFiles.stable.Delete()
		if err != nil {
			return fmt.Errorf("could not delete stable link: %w", err)
		}
	}

	// Delete all package versions
	err = r.Cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository for package %w", err)
	}

	files, err := os.ReadDir(r.rootPath)
	if err != nil {
		return fmt.Errorf("could not read root directory: %w", err)
	}

	if len(files) > 0 {
		return fmt.Errorf("could not delete root directory, not empty after cleanup")
	}

	// Delete the repository directory
	err = os.RemoveAll(r.rootPath)
	if err != nil {
		return fmt.Errorf("could not delete root directory for package %w", err)
	}
	return nil
}

// SetExperiment moves package files from the given source path to the repository and sets it as the experiment.
//
// 1. Cleanup the repository.
// 2. Move the experiment source to the repository.
// 3. Set the experiment link to the experiment package.
func (r *Repository) SetExperiment(ctx context.Context, name string, sourcePath string) error {
	repository, err := readRepository(r.rootPath, r.preRemoveHooks)
	if err != nil {
		return err
	}
	err = repository.cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository: %w", err)
	}
	if !repository.stable.Exists() {
		return fmt.Errorf("stable link does not exist, invalid state")
	}
	if !repository.experiment.Exists() {
		return fmt.Errorf("experiment link does not exist, invalid state")
	}
	err = repository.setExperiment(name, sourcePath)
	if err != nil {
		return fmt.Errorf("could not set experiment: %w", err)
	}
	return nil
}

// PromoteExperiment promotes the experiment to stable.
//
// 1. Cleanup the repository.
// 2. Set the stable link to the experiment package. The experiment link stays in place.
// 3. Cleanup the repository to remove the previous stable package.
func (r *Repository) PromoteExperiment(ctx context.Context) error {
	repository, err := readRepository(r.rootPath, r.preRemoveHooks)
	if err != nil {
		return err
	}
	err = repository.cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository: %w", err)
	}
	if !repository.stable.Exists() {
		return fmt.Errorf("stable link does not exist, invalid state")
	}
	if !repository.experiment.Exists() {
		return fmt.Errorf("experiment link does not exist, invalid state")
	}
	if repository.stable.Target() == repository.experiment.Target() {
		return fmt.Errorf("no experiment to promote")
	}
	err = repository.stable.Set(*repository.experiment.packagePath)
	if err != nil {
		return fmt.Errorf("could not set stable: %w", err)
	}
	err = repository.cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository: %w", err)
	}
	return nil
}

// DeleteExperiment deletes the experiment.
//
// 1. Cleanup the repository.
// 2. Sets the experiment link to the stable link.
// 3. Cleanup the repository to remove the previous experiment package.
func (r *Repository) DeleteExperiment(ctx context.Context) error {
	repository, err := readRepository(r.rootPath, r.preRemoveHooks)
	if err != nil {
		return err
	}
	err = repository.cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository: %w", err)
	}
	if !repository.stable.Exists() {
		return fmt.Errorf("stable link does not exist, invalid state")
	}
	if !repository.experiment.Exists() {
		return fmt.Errorf("experiment link does not exist, invalid state")
	}
	err = repository.setExperimentToStable()
	if err != nil {
		return fmt.Errorf("could not set experiment to stable: %w", err)
	}
	err = repository.cleanup(ctx)
	if err != nil {
		return fmt.Errorf("could not cleanup repository: %w", err)
	}
	return nil
}

// Cleanup calls the cleanup function of the repository
func (r *Repository) Cleanup(ctx context.Context) error {
	repository, err := readRepository(r.rootPath, r.preRemoveHooks)
	if err != nil {
		return err
	}
	return repository.cleanup(ctx)
}

type repositoryFiles struct {
	rootPath       string
	preRemoveHooks map[string]PreRemoveHook

	stable     *link
	experiment *link
}

func readRepository(rootPath string, preRemoveHooks map[string]PreRemoveHook) (*repositoryFiles, error) {
	stableLink, err := newLink(filepath.Join(rootPath, stableVersionLink))
	if err != nil {
		return nil, fmt.Errorf("could not load stable link: %w", err)
	}
	experimentLink, err := newLink(filepath.Join(rootPath, experimentVersionLink))
	if err != nil {
		return nil, fmt.Errorf("could not load experiment link: %w", err)
	}

	return &repositoryFiles{
		rootPath:       rootPath,
		preRemoveHooks: preRemoveHooks,
		stable:         stableLink,
		experiment:     experimentLink,
	}, nil
}

func (r *repositoryFiles) setExperiment(name string, sourcePath string) error {
	path, err := movePackageFromSource(name, r.rootPath, sourcePath)
	if err != nil {
		return fmt.Errorf("could not move experiment source: %w", err)
	}

	return r.experiment.Set(path)
}

// setExperimentToStable moves the experiment to stable.
func (r *repositoryFiles) setExperimentToStable() error {
	return r.experiment.Set(r.stable.linkPath)
}

func (r *repositoryFiles) setStable(name string, sourcePath string) error {
	path, err := movePackageFromSource(name, r.rootPath, sourcePath)
	if err != nil {
		return fmt.Errorf("could not move stable source: %w", err)
	}

	return r.stable.Set(path)
}

func movePackageFromSource(packageName string, rootPath string, sourcePath string) (string, error) {
	if packageName == "" || packageName == stableVersionLink || packageName == experimentVersionLink {
		return "", fmt.Errorf("invalid package name")
	}
	targetPath := filepath.Join(rootPath, packageName)
	_, err := os.Stat(targetPath)
	if err == nil {
		// TODO: Do we want to differentiate between packages that cannot be deleted
		// due to the pre-remove hook and other reasons?
		return "", fmt.Errorf("target package already exists")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("could not stat target package: %w", err)
	}
	if err := paths.SetRepositoryPermissions(sourcePath); err != nil {
		return "", fmt.Errorf("could not set permissions on package: %w", err)
	}
	err = os.Rename(sourcePath, targetPath)
	if err != nil {
		return "", fmt.Errorf("could not move source: %w", err)
	}
	return targetPath, nil
}

func (r *repositoryFiles) cleanup(ctx context.Context) error {
	// migrate old repositories that are missing the experiment link
	if r.stable.Exists() && !r.experiment.Exists() {
		err := r.setExperimentToStable()
		if err != nil {
			return fmt.Errorf("could not migrate old repository without experiment link: %w", err)
		}
	}

	// remove left-over packages
	files, err := os.ReadDir(r.rootPath)
	if err != nil {
		return fmt.Errorf("could not read root directory: %w", err)
	}

	// for all versions that are not stable or experiment:
	// - if no pre-remove hook is configured, delete the package
	// - if a pre-remove hook is configured, run the hook and delete the package only if the hook returns true
	for _, file := range files {
		isLink := file.Name() == stableVersionLink || file.Name() == experimentVersionLink
		isStable := r.stable.Exists() && r.stable.Target() == file.Name()
		isExperiment := r.experiment.Exists() && r.experiment.Target() == file.Name()
		if isLink || isStable || isExperiment {
			continue
		}

		pkgRepositoryPath := filepath.Join(r.rootPath, file.Name())
		pkgName := filepath.Base(r.rootPath)

		if pkgHook, hasHook := r.preRemoveHooks[pkgName]; hasHook {
			canDelete, err := pkgHook(ctx, pkgRepositoryPath)
			if err != nil {
				log.Errorf("Pre-remove hook for package %s returned an error: %v", pkgRepositoryPath, err)
			}
			// if there is an error, the hook still decides if the package can be deleted
			if !canDelete {
				continue
			}
		}

		log.Debugf("Removing package %s", pkgRepositoryPath)
		if err := os.RemoveAll(pkgRepositoryPath); err != nil {
			log.Errorf("could not remove package %s directory, will retry: %v", pkgRepositoryPath, err)
		}
	}

	return nil
}

type link struct {
	linkPath    string
	packagePath *string
}

func newLink(linkPath string) (*link, error) {
	linkExists, err := linkExists(linkPath)
	if err != nil {
		return nil, fmt.Errorf("could check if link exists: %w", err)
	}
	if !linkExists {
		return &link{
			linkPath: linkPath,
		}, nil
	}
	packagePath, err := linkRead(linkPath)
	if err != nil {
		return nil, fmt.Errorf("could not read link: %w", err)
	}
	_, err = os.Stat(packagePath)
	if err != nil {
		return nil, fmt.Errorf("could not read package: %w", err)
	}

	return &link{
		linkPath:    linkPath,
		packagePath: &packagePath,
	}, nil
}

func (l *link) Exists() bool {
	return l.packagePath != nil
}

func (l *link) Target() string {
	if l.Exists() {
		return filepath.Base(*l.packagePath)
	}
	return ""
}

func (l *link) Set(path string) error {
	err := linkSet(l.linkPath, path)
	if err != nil {
		return fmt.Errorf("could not set link: %w", err)
	}
	l.packagePath = &path
	return nil
}

func (l *link) Delete() error {
	err := linkDelete(l.linkPath)
	if err != nil {
		return fmt.Errorf("could not delete link: %w", err)
	}
	l.packagePath = nil
	return nil
}

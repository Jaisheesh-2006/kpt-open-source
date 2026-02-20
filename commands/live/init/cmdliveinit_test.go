// Copyright 2020 The kpt Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package init

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kptdev/kpt/internal/pkg"
	"github.com/kptdev/kpt/internal/testutil"
	kptfilev1 "github.com/kptdev/kpt/pkg/api/kptfile/v1"
	rgfilev1alpha1 "github.com/kptdev/kpt/pkg/api/resourcegroup/v1alpha1"
	"github.com/kptdev/kpt/pkg/kptfile/kptfileutil"
	"github.com/kptdev/kpt/pkg/printer/fake"
	"github.com/stretchr/testify/assert"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

var (
	inventoryName      = "inventory-obj-name"
	inventoryNamespace = "test-namespace"
	inventoryID        = "XXXXXXX-OOOOOOOOOO-XXXX"
)

var kptFile = `
apiVersion: kpt.dev/v1
kind: Kptfile
metadata:
  name: test1
upstreamLock:
  type: git
  git:
    repo: git@github.com:seans3/blueprint-helloworld
    directory: /
    ref: master
`

const testInventoryID = "SSSSSSSSSS-RRRRR"

var kptFileWithInventory = `
apiVersion: kpt.dev/v1
kind: Kptfile
metadata:
  name: test1
upstreamLock:
  type: git
  git:
    repo: git@github.com:seans3/blueprint-helloworld
    directory: /
    ref: master
inventory:
    name: foo
    namespace: test-namespace
    inventoryID: ` + testInventoryID + "\n"

var resourceGroupInventory = `
apiVersion: kpt.dev/v1alpha1
kind: ResourceGroup
metadata:
  name: foo
  namespace: test-namespace
`

// testHash is a helper producing the expected deterministic hash.
func testHash(namespace, name string) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s:%s", namespace, name)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestCmd_generateHash(t *testing.T) {
	testCases := map[string]struct {
		namespace string
		name      string
		expected  string
		isError   bool
	}{
		"Empty namespace is an error": {
			name:      inventoryName,
			namespace: "",
			isError:   true,
		},
		"Empty name is an error": {
			name:      "",
			namespace: inventoryNamespace,
			isError:   true,
		},
		"Namespace/name hash is deterministic": {
			name:      inventoryName,
			namespace: inventoryNamespace,
			expected:  testHash(inventoryNamespace, inventoryName),
			isError:   false,
		},
		"Same inputs produce same hash": {
			name:      "my-app",
			namespace: "default",
			expected:  testHash("default", "my-app"),
			isError:   false,
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			actual, err := generateHash(tc.namespace, tc.name)
			if tc.isError {
				if err == nil {
					t.Fatalf("expected error but received none")
				}
				return
			}
			assert.NoError(t, err)
			if tc.expected != actual {
				t.Errorf("expecting generated hash (%s), got (%s)", tc.expected, actual)
			}
			// Verify determinism: calling again yields the same result.
			again, err := generateHash(tc.namespace, tc.name)
			assert.NoError(t, err)
			assert.Equal(t, actual, again, "generateHash must be deterministic")
			// Verify length: SHA-1 hex is always 40 chars.
			assert.Len(t, actual, 40)
		})
	}
}

func TestCmd_Run(t *testing.T) {
	testCases := map[string]struct {
		kptfile           string
		resourcegroup     string
		rgfilename        string
		name              string
		namespace         string
		inventoryID       string
		force             bool
		expectedErrorMsg  string
		expectAutoGenID   bool
		expectedInventory kptfilev1.Inventory
	}{
		"Fields are defaulted if not provided": {
			kptfile:         kptFile,
			name:            "",
			rgfilename:      "resourcegroup.yaml",
			namespace:       "testns",
			inventoryID:     "",
			expectAutoGenID: true,
			expectedInventory: kptfilev1.Inventory{
				Namespace: "testns",
				Name:      "inventory-*",
			},
		},
		"Provided values are used": {
			kptfile:     kptFile,
			rgfilename:  "custom-rg.yaml",
			name:        "my-pkg",
			namespace:   "my-ns",
			inventoryID: "my-inv-id",
			expectedInventory: kptfilev1.Inventory{
				Namespace:   "my-ns",
				Name:        "my-pkg",
				InventoryID: "my-inv-id",
			},
		},
		"Provided values are used with custom resourcegroup filename": {
			kptfile:     kptFile,
			rgfilename:  "custom-rg.yaml",
			name:        "my-pkg",
			namespace:   "my-ns",
			inventoryID: "my-inv-id",
			expectedInventory: kptfilev1.Inventory{
				Namespace:   "my-ns",
				Name:        "my-pkg",
				InventoryID: "my-inv-id",
			},
		},
		"Kptfile with inventory already set is error": {
			kptfile:          kptFileWithInventory,
			name:             inventoryName,
			rgfilename:       "custom-rg.yaml",
			namespace:        inventoryNamespace,
			inventoryID:      inventoryID,
			force:            false,
			expectedErrorMsg: "inventory information already set",
		},
		"ResourceGroup with inventory already set is error": {
			kptfile:          kptFile,
			resourcegroup:    resourceGroupInventory,
			rgfilename:       "resourcegroup.yaml",
			name:             inventoryName,
			namespace:        inventoryNamespace,
			inventoryID:      inventoryID,
			force:            false,
			expectedErrorMsg: "inventory information already set for package",
		},
		"ResourceGroup with inventory and Kptfile with inventory already set is error": {
			kptfile:          kptFileWithInventory,
			resourcegroup:    resourceGroupInventory,
			rgfilename:       "resourcegroup.yaml",
			name:             inventoryName,
			namespace:        inventoryNamespace,
			inventoryID:      inventoryID,
			force:            false,
			expectedErrorMsg: "inventory information already set",
		},
		"The force flag allows changing inventory information even if already set in Kptfile": {
			kptfile:     kptFileWithInventory,
			name:        inventoryName,
			rgfilename:  "resourcegroup.yaml",
			namespace:   inventoryNamespace,
			inventoryID: inventoryID,
			force:       true,
			expectedInventory: kptfilev1.Inventory{
				Namespace:   inventoryNamespace,
				Name:        inventoryName,
				InventoryID: inventoryID,
			},
		},
		"The force flag allows changing inventory information even if already set in ResourceGroup": {
			kptfile:       kptFile,
			resourcegroup: resourceGroupInventory,
			rgfilename:    "resourcegroup.yaml",
			name:          inventoryName,
			namespace:     inventoryNamespace,
			inventoryID:   inventoryID,
			force:         true,
			expectedInventory: kptfilev1.Inventory{
				Namespace:   inventoryNamespace,
				Name:        inventoryName,
				InventoryID: inventoryID,
			},
		},
		"Invalid DNS name is rejected": {
			kptfile:          kptFile,
			name:             "My_App!",
			rgfilename:       "resourcegroup.yaml",
			namespace:        "testns",
			inventoryID:      "",
			expectedErrorMsg: "not a valid Kubernetes resource name",
		},
	}

	for tn, tc := range testCases {
		t.Run(tn, func(t *testing.T) {
			// Set up fake test factory
			tf := cmdtesting.NewTestFactory().WithNamespace(tc.namespace)
			defer tf.Cleanup()
			ioStreams, _, _, _ := genericclioptions.NewTestIOStreams() //nolint:dogsled

			w, clean := testutil.SetupWorkspace(t)
			defer clean()
			err := os.WriteFile(filepath.Join(w.WorkspaceDirectory, kptfilev1.KptFileName),
				[]byte(tc.kptfile), 0600)
			if !assert.NoError(t, err) {
				t.FailNow()
			}

			// Create ResourceGroup file if testing the STDIN feature.
			if tc.resourcegroup != "" && tc.rgfilename != "" {
				err := os.WriteFile(filepath.Join(w.WorkspaceDirectory, tc.rgfilename),
					[]byte(tc.resourcegroup), 0600)
				if !assert.NoError(t, err) {
					t.FailNow()
				}
			}

			revert := testutil.Chdir(t, w.WorkspaceDirectory)
			defer revert()

			runner := NewRunner(fake.CtxWithDefaultPrinter(), tf, ioStreams)
			runner.RGFileName = tc.rgfilename
			args := []string{
				"--name", tc.name,
				"--inventory-id", tc.inventoryID,
			}
			if tc.force {
				args = append(args, "--force")
			}
			runner.Command.SetArgs(args)

			err = runner.Command.Execute()

			// Check if there should be an error
			if tc.expectedErrorMsg != "" {
				if !assert.Error(t, err) {
					t.FailNow()
				}
				assert.Contains(t, err.Error(), tc.expectedErrorMsg)
				return
			}

			// Otherwise, validate the kptfile values and/or resourcegroup values.
			var actualInv kptfilev1.Inventory
			assert.NoError(t, err)
			kf, err := kptfileutil.ReadKptfile(filesys.FileSystemOrOnDisk{}, w.WorkspaceDirectory)
			assert.NoError(t, err)

			switch tc.rgfilename {
			case "":
				if !assert.NotNil(t, kf.Inventory) {
					t.FailNow()
				}
				actualInv = *kf.Inventory
			default:
				// Check resourcegroup file if testing the STDIN feature.
				rg, err := pkg.ReadRGFile(w.WorkspaceDirectory, tc.rgfilename)
				assert.NoError(t, err)
				if !assert.NotNil(t, rg) {
					t.FailNow()
				}

				// Convert resourcegroup inventory back to Kptfile structure so we can share assertion
				// logic for Kptfile inventory and ResourceGroup inventory structure.
				actualInv = kptfilev1.Inventory{
					Name:        rg.Name,
					Namespace:   rg.Namespace,
					InventoryID: rg.Labels[rgfilev1alpha1.RGInventoryIDLabel],
				}
			}

			if tc.expectedInventory.Name != "" && tc.expectedInventory.Name != "inventory-*" {
				assert.Equal(t, tc.expectedInventory.Name, actualInv.Name)
			}
			assert.Equal(t, tc.expectedInventory.Namespace, actualInv.Namespace)
			if tc.expectAutoGenID {
				// Auto-generated ID should be a 40-char SHA-1 hex digest.
				assert.Len(t, actualInv.InventoryID, 40, "auto-generated ID should be a SHA-1 hex digest")
			} else {
				assert.Equal(t, tc.expectedInventory.InventoryID, actualInv.InventoryID)
			}
		})
	}
}

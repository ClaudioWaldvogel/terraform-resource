package out_test

import (
	"crypto/md5"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"time"

	"terraform-resource/models"
	"terraform-resource/out"
	"terraform-resource/test/helpers"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Out Plan", func() {

	var (
		backendType   string
		backendConfig map[string]interface{}
		envName       string
		stateFilePath string
		planFilePath  string
		s3ObjectPath  string
		workingDir    string
		workspacePath string
	)

	BeforeEach(func() {
		region := os.Getenv("AWS_REGION") // optional
		if region == "" {
			region = "us-east-1"
		}

		workspacePath = helpers.RandomString("out-backend-test")

		envName = helpers.RandomString("out-test")
		stateFilePath = path.Join(workspacePath, envName, "terraform.tfstate")
		planFilePath = path.Join(workspacePath, fmt.Sprintf("%s-plan", envName), "terraform.tfstate")
		s3ObjectPath = path.Join(bucketPath, helpers.RandomString("out-test"))

		backendType = "s3"
		backendConfig = map[string]interface{}{
			"bucket":               bucket,
			"key":                  "terraform.tfstate",
			"access_key":           accessKey,
			"secret_key":           secretKey,
			"region":               region,
			"workspace_key_prefix": workspacePath,
		}

		var err error
		workingDir, err = ioutil.TempDir(os.TempDir(), "terraform-resource-out-test")
		Expect(err).ToNot(HaveOccurred())

		// ensure relative paths resolve correctly
		err = os.Chdir(workingDir)
		Expect(err).ToNot(HaveOccurred())

		fixturesDir := path.Join(helpers.ProjectRoot(), "fixtures")
		err = exec.Command("cp", "-r", fixturesDir, workingDir).Run()
		Expect(err).ToNot(HaveOccurred())

		err = downloadStatefulPlugin(workingDir)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(workingDir)
		awsVerifier.DeleteObjectFromS3(bucket, s3ObjectPath)
		awsVerifier.DeleteObjectFromS3(bucket, planFilePath)
		awsVerifier.DeleteObjectFromS3(bucket, stateFilePath)
	})

	It("plan infrastructure and apply it", func() {
		planOutRequest := models.OutRequest{
			Source: models.Source{
				Terraform: models.Terraform{
					BackendType:   backendType,
					BackendConfig: backendConfig,
				},
			},
			Params: models.OutParams{
				EnvName: envName,
				Terraform: models.Terraform{
					Source:   "fixtures/aws/",
					PlanOnly: true,
					Env: map[string]string{
						"HOME": workingDir, // in prod plugin is installed system-wide
					},
					Vars: map[string]interface{}{
						"access_key":     accessKey,
						"secret_key":     secretKey,
						"bucket":         bucket,
						"object_key":     s3ObjectPath,
						"object_content": "terraform-is-neat",
						"region":         region,
					},
				},
			},
		}

		applyRequest := models.OutRequest{
			Source: models.Source{
				Terraform: models.Terraform{
					BackendType:   backendType,
					BackendConfig: backendConfig,
				},
			},
			Params: models.OutParams{
				EnvName: envName,
				Terraform: models.Terraform{
					Source:  "fixtures/aws/",
					PlanRun: true,
					Env: map[string]string{
						"HOME": workingDir, // in prod plugin is installed system-wide
					},
				},
			},
		}

		By("running 'out' to create the plan file")

		planrunner := out.Runner{
			SourceDir: workingDir,
			LogWriter: GinkgoWriter,
		}
		planOutput, err := planrunner.Run(planOutRequest)
		Expect(err).ToNot(HaveOccurred())

		By("ensuring that plan file exists")

		awsVerifier.ExpectS3FileToExist(
			bucket,
			planFilePath,
		)
		defer awsVerifier.DeleteObjectFromS3(bucket, planFilePath)

		Expect(planOutput.Version.EnvName).To(Equal(planOutRequest.Params.EnvName))
		Expect(planOutput.Version.PlanOnly).To(Equal("true"), "Expected PlanOnly to be true, but was false")

		By("ensuring s3 file does not already exist")

		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			s3ObjectPath,
		)

		By("applying the plan")

		applyrunner := out.Runner{
			SourceDir: workingDir,
			LogWriter: GinkgoWriter,
		}
		createOutput, err := applyrunner.Run(applyRequest)
		Expect(err).ToNot(HaveOccurred())

		Expect(createOutput.Version.PlanOnly).To(BeEmpty())

		Expect(createOutput.Metadata).ToNot(BeEmpty())
		fields := map[string]interface{}{}
		for _, field := range createOutput.Metadata {
			fields[field.Name] = field.Value
		}
		Expect(fields["env_name"]).To(Equal(envName))
		expectedMD5 := fmt.Sprintf("%x", md5.Sum([]byte("terraform-is-neat")))
		Expect(fields["content_md5"]).To(Equal(expectedMD5))

		awsVerifier.ExpectS3FileToExist(
			bucket,
			s3ObjectPath,
		)

		By("ensuring that plan file no longer exists after the apply")

		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			planFilePath,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	It("takes the existing statefile into account when generating a plan", func() {
		initialApplyRequest := models.OutRequest{
			Source: models.Source{
				Terraform: models.Terraform{
					BackendType:   backendType,
					BackendConfig: backendConfig,
				},
			},
			Params: models.OutParams{
				EnvName: envName,
				Terraform: models.Terraform{
					Source: "fixtures/aws/",
					Env: map[string]string{
						"HOME": workingDir, // in prod plugin is installed system-wide
					},
					Vars: map[string]interface{}{
						"access_key":     accessKey,
						"secret_key":     secretKey,
						"bucket":         bucket,
						"object_key":     s3ObjectPath,
						"object_content": "terraform-is-neat",
						"region":         region,
					},
				},
			},
		}

		planRequest := models.OutRequest{
			Source: models.Source{
				Terraform: models.Terraform{
					BackendType:   backendType,
					BackendConfig: backendConfig,
				},
			},
			Params: models.OutParams{
				EnvName: envName,
				Terraform: models.Terraform{
					Source:   "fixtures/aws/",
					PlanOnly: true,
					Env: map[string]string{
						"HOME": workingDir, // in prod plugin is installed system-wide
					},
					Vars: map[string]interface{}{
						"access_key":     accessKey,
						"secret_key":     secretKey,
						"bucket":         bucket,
						"object_key":     s3ObjectPath,
						"object_content": "terraform-is-neat",
						"region":         region,
					},
				},
			},
		}

		applyPlanRequest := models.OutRequest{
			Source: models.Source{
				Terraform: models.Terraform{
					BackendType:   backendType,
					BackendConfig: backendConfig,
				},
			},
			Params: models.OutParams{
				EnvName: envName,
				Terraform: models.Terraform{
					Source:  "fixtures/aws/",
					PlanRun: true,
					Env: map[string]string{
						"HOME": workingDir, // in prod plugin is installed system-wide
					},
				},
			},
		}

		By("ensuring plan and state files does not already exist")

		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			stateFilePath,
		)
		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			planFilePath,
		)

		By("running 'out' to create the statefile")

		runner := out.Runner{
			SourceDir: workingDir,
			LogWriter: GinkgoWriter,
		}
		_, err := runner.Run(initialApplyRequest)
		Expect(err).ToNot(HaveOccurred())

		By("ensuring that statefile exists and plan does not")

		awsVerifier.ExpectS3FileToExist(
			bucket,
			stateFilePath,
		)
		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			planFilePath,
		)

		initialLastModified := awsVerifier.GetLastModifiedFromS3(bucket, s3ObjectPath)

		time.Sleep(1 * time.Second) // ensure LastModified has time to change

		By("creating the plan")

		_, err = runner.Run(planRequest)
		Expect(err).ToNot(HaveOccurred())

		awsVerifier.ExpectS3FileToExist(
			bucket,
			s3ObjectPath,
		)

		By("ensuring that state and plan files exist")

		awsVerifier.ExpectS3FileToExist(
			bucket,
			stateFilePath,
		)
		awsVerifier.ExpectS3FileToExist(
			bucket,
			planFilePath,
		)

		By("applying the plan")

		_, err = runner.Run(applyPlanRequest)
		Expect(err).ToNot(HaveOccurred())

		By("ensuring that existing statefile was used and S3 Object was unchanged")
		finalLastModified := awsVerifier.GetLastModifiedFromS3(bucket, s3ObjectPath)
		Expect(finalLastModified).To(Equal(initialLastModified))
	})

	It("plan should be deleted on destroy", func() {
		planOutRequest := models.OutRequest{
			Source: models.Source{
				Terraform: models.Terraform{
					BackendType:   backendType,
					BackendConfig: backendConfig,
				},
			},
			Params: models.OutParams{
				EnvName: envName,
				Terraform: models.Terraform{
					Source:   "fixtures/aws/",
					PlanOnly: true,
					Env: map[string]string{
						"HOME": workingDir, // in prod plugin is installed system-wide
					},
					Vars: map[string]interface{}{
						"access_key":     accessKey,
						"secret_key":     secretKey,
						"bucket":         bucket,
						"object_key":     s3ObjectPath,
						"object_content": "terraform-is-neat",
						"region":         region,
					},
				},
			},
		}

		By("ensuring state and plan file does not already exist")

		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			planFilePath,
		)

		By("running 'out' to create the plan file")

		planrunner := out.Runner{
			SourceDir: workingDir,
			LogWriter: GinkgoWriter,
		}
		_, err := planrunner.Run(planOutRequest)
		Expect(err).ToNot(HaveOccurred())

		By("ensuring that plan file exists with valid version (LastModified)")

		awsVerifier.ExpectS3FileToExist(
			bucket,
			planFilePath,
		)

		By("running 'out' to delete the plan file")

		planOutRequest.Params.Terraform.PlanOnly = false
		planOutRequest.Params.Action = models.DestroyAction
		_, err = planrunner.Run(planOutRequest)
		Expect(err).ToNot(HaveOccurred())

		By("ensuring that plan file no longer exists")

		awsVerifier.ExpectS3FileToNotExist(
			bucket,
			planFilePath,
		)
	})
})

func downloadStatefulPlugin(workingDir string) error {
	var hostOS string
	if runtime.GOOS == "darwin" {
		hostOS = "darwin"
	} else {
		hostOS = "linux"
	}
	url := fmt.Sprintf("https://github.com/ashald/terraform-provider-stateful/releases/download/v1.0.0/terraform-provider-stateful_v1.0.0-%s-amd64", hostOS)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	err = os.MkdirAll(filepath.Join(workingDir, ".terraform.d", "plugins"), os.ModePerm)
	if err != nil {
		return err
	}

	pluginPath := filepath.Join(workingDir, ".terraform.d", "plugins", "terraform-provider-stateful")
	out, err := os.Create(pluginPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if err = out.Chmod(0755); err != nil {
		return err
	}

	_, err = io.Copy(out, resp.Body)
	return err
}

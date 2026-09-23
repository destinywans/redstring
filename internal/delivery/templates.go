package delivery

type Template struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	RequiredInputs []string   `json:"required_inputs"`
	Definition     Definition `json:"definition"`
}

// Templates are executable Go examples, not a promise to infer arbitrary
// repositories. The source and reports cross Pods as real archived artifacts.
func Templates() []Template {
	source := Node{ID: "source", Name: "检出不可变源码", Kind: "container", Image: "golang:1.26.8-bookworm", Command: []string{"sh", "-ec"}, Args: []string{sourceScript, "--", "{{inputs.parameters.repository}}", "{{inputs.parameters.revision}}"}, Inputs: []Input{{Name: "repository"}, {Name: "revision"}}, Outputs: []Output{{Name: "commit-sha", Path: "/workspace/commit-sha"}}, Artifacts: []FileArtifact{{Name: "source", Path: "/workspace/source", Archive: "tar"}}, TimeoutSeconds: 600}
	check := func(id, name, command string) Node {
		return Node{ID: id, Name: name, Kind: "container", Image: "golang:1.26.8-bookworm", Command: []string{"sh", "-ec"}, Args: []string{"set -eu; export HOME=/tmp GOCACHE=/tmp/go-cache GOPATH=/tmp/go; cd /workspace/source; " + command + " > /workspace/report.txt 2>&1 || { cat /workspace/report.txt; exit 1; }; cat /workspace/report.txt; printf '\ncheck passed\n' >> /workspace/report.txt"}, DependsOn: []Dependency{{NodeID: "source"}}, ArtifactInputs: []FileArtifactInput{{Name: "source", Path: "/workspace/source", FromNode: "source", Artifact: "source"}}, Artifacts: []FileArtifact{{Name: "report", Path: "/workspace/report.txt"}}, TimeoutSeconds: 900}
	}
	unit, vet := check("unit", "单元测试", "go test ./..."), check("vet", "静态检查", "go vet ./...")
	imageBuild := Node{ID: "build", Name: "构建并发布镜像", Kind: "container", Image: "moby/buildkit:v0.24.0-rootless", Command: []string{"sh", "-ec"}, Args: []string{imageBuildScript, "--", "{{inputs.parameters.revision}}", "{{inputs.parameters.image}}", "{{inputs.parameters.buildkit}}", "{{inputs.parameters.insecure-registry}}"}, Inputs: []Input{{Name: "revision", FromNode: "source", Output: "commit-sha"}, {Name: "image"}, {Name: "buildkit"}, {Name: "insecure-registry", Value: "false"}}, Outputs: []Output{{Name: "commit-sha", Path: "/workspace/commit-sha"}, {Name: "artifact-digest", Path: "/workspace/artifact-digest"}, {Name: "artifact-url", Path: "/workspace/artifact-url"}}, Artifacts: []FileArtifact{{Name: "build-metadata", Path: "/workspace/image.json"}}, TimeoutSeconds: 1800}
	binaryBuild := Node{ID: "build", Name: "编译与归档二进制", Kind: "container", Image: "golang:1.26.8-bookworm", Command: []string{"sh", "-ec"}, Args: []string{binaryBuildScript, "--", "{{inputs.parameters.revision}}", "{{inputs.parameters.go-package}}", "{{inputs.parameters.goarch}}"}, Inputs: []Input{{Name: "revision", FromNode: "source", Output: "commit-sha"}, {Name: "go-package", Value: "."}, {Name: "goarch", Value: "arm64"}}, Outputs: []Output{{Name: "commit-sha", Path: "/workspace/commit-sha"}, {Name: "artifact-digest", Path: "/workspace/artifact-digest"}}, Artifacts: []FileArtifact{{Name: "package", Path: "/workspace/application.tar.gz"}}, TimeoutSeconds: 1800}
	makeRelease := func(id, name, description string, build Node, inputs []string) Template {
		build.DependsOn = []Dependency{{NodeID: "source"}, {NodeID: "unit"}, {NodeID: "vet"}}
		build.ArtifactInputs = []FileArtifactInput{{Name: "source", Path: "/workspace/source", FromNode: "source", Artifact: "source"}, {Name: "unit-report", Path: "/workspace/unit.txt", FromNode: "unit", Artifact: "report"}, {Name: "vet-report", Path: "/workspace/vet.txt", FromNode: "vet", Artifact: "report"}}
		return Template{ID: id, Name: name, Description: description, RequiredInputs: inputs, Definition: Definition{Provider: "native", Nodes: []Node{source, unit, vet, build, {ID: "approval", Name: "发布审批", Kind: "approval", SourceNodeID: "build", TargetID: "select-registered-target", DependsOn: []Dependency{{NodeID: "build"}}}, {ID: "deploy", Name: "发布到登记目标", Kind: "deploy", ApprovalNodeID: "approval", DependsOn: []Dependency{{NodeID: "approval"}}}, {ID: "health", Name: "持续恢复验证", Kind: "health", DeployNodeID: "deploy", DependsOn: []Dependency{{NodeID: "deploy"}}}}}}
	}
	return []Template{
		makeRelease("kubernetes-gitops", "Kubernetes GitOps 发布", "检出源码、并行测试与检查、BuildKit 构建镜像、人工批准、GitOps 发布与持续恢复验证。", imageBuild, []string{"repository", "revision", "image", "buildkit", "target_id"}),
		makeRelease("vm-compose", "VM Compose 发布", "真实制品在源码、并行检查与构建节点间传递，人工批准后发布到登记的 Compose project。", imageBuild, []string{"repository", "revision", "image", "buildkit", "target_id"}),
		makeRelease("vm-systemd", "VM systemd 发布", "检出与并行检查后编译 Go，归档带 SHA256 的发布包，批准后发布到登记的 systemd unit 并持续验证。", binaryBuild, []string{"repository", "revision", "go-package", "goarch", "target_id"}),
	}
}

const sourceScript = `set -eu
export HOME=/tmp
repository=$1
revision=$2
test -n "$repository" && test -n "$revision"
case "$revision" in *[!0-9a-f]*) echo 'revision must be an immutable Git SHA' >&2; exit 2;; esac
test "${#revision}" -eq 40 || test "${#revision}" -eq 64
git clone --no-checkout "$repository" /workspace/source
cd /workspace/source
git checkout --detach "$revision"
test "$(git rev-parse HEAD)" = "$revision"
printf '%s' "$revision" > /workspace/commit-sha
`
const imageBuildScript = `set -eu
export HOME=/tmp DOCKER_CONFIG=/tmp/docker
revision=$1
image=$2
buildkit=$3
insecure=$4
test -s /workspace/unit.txt && test -s /workspace/vet.txt
case "$revision" in *[!0-9a-f]*) exit 2;; esac
test "${#revision}" -eq 40 || test "${#revision}" -eq 64
case "$image" in ''|*','*|*' '*|*'@'*) echo 'invalid image repository' >&2; exit 2;; esac
test -n "$buildkit"
output="type=image,name=$image:ci-$revision,push=true"
case "$insecure" in true) output="$output,registry.insecure=true";; false) :;; *) exit 2;; esac
buildctl --addr "$buildkit" build --frontend dockerfile.v0 --local context=/workspace/source --local dockerfile=/workspace/source --opt filename=Dockerfile --output "$output" --metadata-file /workspace/image.json
digest=$(sed -n 's/.*"containerimage.digest": *"\(sha256:[a-f0-9]*\)".*/\1/p' /workspace/image.json | head -n 1)
test "${#digest}" -eq 71
printf '%s' "$revision" > /workspace/commit-sha
printf '%s' "$digest" > /workspace/artifact-digest
printf '%s@%s' "$image" "$digest" > /workspace/artifact-url
`
const binaryBuildScript = `set -eu
revision=$1
package=$2
architecture=$3
test -s /workspace/unit.txt && test -s /workspace/vet.txt
case "$revision" in *[!0-9a-f]*) exit 2;; esac
test "${#revision}" -eq 40 || test "${#revision}" -eq 64
case "$architecture" in amd64|arm64) :;; *) exit 2;; esac
export HOME=/tmp GOCACHE=/tmp/go-cache GOPATH=/tmp/go
cd /workspace/source
test "$(git rev-parse HEAD)" = "$revision"
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -o /workspace/aiops-demo "$package"
tar --format=ustar -czf /workspace/application.tar.gz -C /workspace aiops-demo
printf '%s' "$revision" > /workspace/commit-sha
printf 'sha256:%s' "$(sha256sum /workspace/application.tar.gz | cut -d ' ' -f 1)" > /workspace/artifact-digest
`

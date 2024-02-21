IMAGE=quiq/docker-registry-ui
VERSION=`sed -n '/version/ s/.* = //;s/"//g p' version.go`

.DEFAULT: buildx

buildx:
	@docker build -t ${IMAGE}:${VERSION} .

publish:
	@docker buildx build --platform linux/amd64,linux/arm64 -t ${IMAGE}:${VERSION} -t ${IMAGE}:latest --push .

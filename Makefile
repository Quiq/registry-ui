IMAGE=quiq/docker-registry-ui
VERSION=`sed -n '/version/ s/.* = //;s/"//g p' version.go`

.DEFAULT: build

build:
	@docker build -t ${IMAGE}:${VERSION} .
	@echo
	@echo "The image has been built: ${IMAGE}:${VERSION}"
	@echo

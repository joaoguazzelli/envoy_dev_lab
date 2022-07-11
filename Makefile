.PHONY: pb
pb:
	protoc --proto_path ./api/ --go_out=Mecho/v1/messages/messages.proto=github.com/joaoguazzelli/envoy_dev_lab/pkg/echo_v1/messages,module=github.com/joaoguazzelli/envoy_dev_lab:. echo/v1/messages/messages.proto echo/v1/echo.proto echo/v1/auth_service.proto
	protoc --proto_path ./api/ --go-grpc_out=Mecho/v1/messages/messages.proto=github.com/joaoguazzelli/envoy_dev_lab/pkg/echo_v1/messages,module=github.com/joaoguazzelli/envoy_dev_lab:. echo/v1/messages/messages.proto echo/v1/echo.proto echo/v1/auth_service.proto
# 
### kubernetes related targets

.PHONY: .common_deploy
.common_deploy:
	GOOS=linux go build -o $(CURDIR)/cmd/$(dir)/$(deploy) $(CURDIR)/cmd/$(dir)
	eval $$(minikube docker-env)
	docker build -t $(deploy):latest $(CURDIR)/cmd/$(dir)
	helm upgrade \
		--install $(deploy) \
		--atomic --debug --reset-values \
		--timeout 30s \
		--kube-context minikube \
		--namespace default \
		-f $(CURDIR)/deploy/playground/values_$(deploy).yaml $(CURDIR)/deploy/playground/


.PHONY: xds-server
xds-server:
	@$(MAKE) .common_deploy deploy=$@ dir=xds_k8s

.PHONY: backend
backend:
	@$(MAKE) .common_deploy deploy=$@ dir=server

.PHONY: frontend
frontend:
	@$(MAKE) .common_deploy deploy=$@ dir=client

.PHONY: deploy
deploy: backend xds-server frontend

.PHONY: undeploy
undeploy:
	helm uninstall frontend
	helm uninstall xds-server
	helm uninstall backend

### kubernetes related targe
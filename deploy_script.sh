#!/bin/bash

eval $(minikube docker-env)
make deploy
echo "------ SUCESSFULLY DEPLOYED ------"
echo "------ XDS SERVER LOGS ------"
kubectl logs -lapp=xds-server
echo "------ END XDS SERVER LOGS ------"
echo "------ FRONTEND LOGS ------"
kubectl logs -lapp=frontend
echo "------ END FRONTEND LOGS ------"
echo "------ BACKEND LOGS ------"
kubectl logs -lapp=backend
echo "------ END BACKEND LOGS ------"

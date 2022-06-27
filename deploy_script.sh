#!/bin/bash
Green='\033[0;32m'
NC='\033[0m'
eval $(minikube docker-env)
make deploy
echo -e "${Green}------ SUCESSFULLY DEPLOYED ------${NC}"
echo -e "${Green}------ XDS SERVER LOGS ------${NC}"
kubectl logs -lapp=xds-server
echo -e "${Green}------ END XDS SERVER LOGS ------${NC}"
echo -e "${Green}------ FRONTEND LOGS ------${NC}"
kubectl logs -lapp=frontend
echo -e "${Green}------ END FRONTEND LOGS ------${NC}"
echo -e "${Green}------ BACKEND LOGS ------${NC}"
kubectl logs -lapp=backend
echo -e "${Green}------ END BACKEND LOGS ------${NC}"

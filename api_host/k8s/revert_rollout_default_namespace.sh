#!/bin/bash
kubectl delete deployment/api-host service/api-host ingress/api-host \
  networkpolicy/api-host-ocr-isolated rolebinding/api-host role/api-host \
  serviceaccount/api-host -n "default"
kubectl delete pvc api-host-data api-host-state -n "default"

#!/usr/bin/env bash
# Supprime le cluster et le registre des démonstrations
kind delete cluster --name formation
docker rm -f kind-registry

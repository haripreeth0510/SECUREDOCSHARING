#!/usr/bin/env bash
# Creates the Azure resources the plan needs: Blob Storage (private) and Key Vault (RBAC).
# Prerequisites: Azure CLI installed (brew install azure-cli) and `az login` done.
# Not yet run by the author of this script: read it first, run it step by step.
set -euo pipefail

LOCATION="${LOCATION:-centralindia}"
RG="${RG:-rg-securedocsharing-dev}"
SUFFIX="${SUFFIX:-$RANDOM}"
STORAGE="sdsdev${SUFFIX}"          # 3-24 chars, lowercase letters and digits, globally unique
KV="kv-sds-dev-${SUFFIX}"          # 3-24 chars, globally unique
CONTAINER="documents"
KEY_NAME="document-kek"

echo "Resource group: $RG  Location: $LOCATION"
az group create --name "$RG" --location "$LOCATION" >/dev/null

echo "Creating storage account $STORAGE (private, TLS 1.2+)"
az storage account create \
  --name "$STORAGE" --resource-group "$RG" --location "$LOCATION" \
  --sku Standard_LRS --kind StorageV2 \
  --min-tls-version TLS1_2 --allow-blob-public-access false >/dev/null

echo "Creating Key Vault $KV (RBAC, purge protection)"
az keyvault create \
  --name "$KV" --resource-group "$RG" --location "$LOCATION" \
  --enable-rbac-authorization true --enable-purge-protection true >/dev/null

ME=$(az ad signed-in-user show --query id -o tsv)
STORAGE_ID=$(az storage account show --name "$STORAGE" --resource-group "$RG" --query id -o tsv)
KV_ID=$(az keyvault show --name "$KV" --query id -o tsv)

echo "Granting your user data-plane roles (needed for local development)"
az role assignment create --assignee "$ME" --role "Storage Blob Data Contributor" --scope "$STORAGE_ID" >/dev/null
az role assignment create --assignee "$ME" --role "Key Vault Crypto Officer" --scope "$KV_ID" >/dev/null

echo "Waiting for role assignments to propagate..."
sleep 60

az storage container create --account-name "$STORAGE" --name "$CONTAINER" --auth-mode login >/dev/null
az keyvault key create --vault-name "$KV" --name "$KEY_NAME" --kty RSA --size 3072 >/dev/null

cat <<OUT

Done. Add these to your .env:
AZURE_STORAGE_ACCOUNT=$STORAGE
AZURE_STORAGE_CONTAINER=$CONTAINER
AZURE_KEYVAULT_URL=https://$KV.vault.azure.net/
AZURE_KEK_NAME=$KEY_NAME

To delete everything later:  az group delete --name $RG --yes
OUT
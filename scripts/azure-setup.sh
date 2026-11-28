#!/usr/bin/env bash
# Creates the Azure resources the plan needs: Blob Storage (private) and Key Vault (RBAC).
# Prerequisites: Azure CLI installed (brew install azure-cli) and `az login` done.
# Not yet run against a real subscription: read it first. It asks before creating anything.
set -euo pipefail

command -v az >/dev/null || { echo "Azure CLI not found: brew install azure-cli, then az login"; exit 1; }
az account show >/dev/null 2>&1 || { echo "Not logged in: run az login"; exit 1; }
echo "Subscription: $(az account show --query name -o tsv)"
read -r -p "Create billable resources in this subscription? [y/N] " ok
[[ "$ok" == "y" || "$ok" == "Y" ]] || exit 1

LOCATION="${LOCATION:-centralindia}"
RG="${RG:-rg-securedocsharing-dev}"
SUFFIX="${SUFFIX:-$RANDOM}"
STORAGE="sdsdev${SUFFIX}"          # 3-24 chars, lowercase letters and digits, globally unique
KV="kv-sds-dev-${SUFFIX}"          # 3-24 chars, globally unique
CONTAINER="documents"
KEY_NAME="document-kek"

echo "Resource group: $RG  Location: $LOCATION"
az group create --name "$RG" --location "$LOCATION" >/dev/null

echo "Creating storage account $STORAGE (private, HTTPS only, TLS 1.2+, keyless)"
# --allow-shared-key-access false: account keys and SAS signed with them stop
# working; every caller must authenticate with Entra ID (Managed Identity / az login).
az storage account create \
  --name "$STORAGE" --resource-group "$RG" --location "$LOCATION" \
  --sku Standard_LRS --kind StorageV2 \
  --https-only true --min-tls-version TLS1_2 \
  --allow-blob-public-access false --allow-shared-key-access false >/dev/null

echo "Creating Key Vault $KV (RBAC, purge protection, 7-day soft delete)"
# Purge protection cannot be turned off again. With 7-day retention (the minimum)
# a deleted dev vault blocks its name for a week instead of the 90-day default.
az keyvault create \
  --name "$KV" --resource-group "$RG" --location "$LOCATION" \
  --enable-rbac-authorization true --enable-purge-protection true \
  --retention-days 7 >/dev/null

ME=$(az ad signed-in-user show --query id -o tsv)
STORAGE_ID=$(az storage account show --name "$STORAGE" --resource-group "$RG" --query id -o tsv)
KV_ID=$(az keyvault show --name "$KV" --query id -o tsv)

echo "Granting your user data-plane roles (needed for local development)"
az role assignment create --assignee "$ME" --role "Storage Blob Data Contributor" --scope "$STORAGE_ID" >/dev/null
az role assignment create --assignee "$ME" --role "Key Vault Crypto Officer" --scope "$KV_ID" >/dev/null

echo "Waiting for role assignments to propagate..."
sleep 60

az storage container create --account-name "$STORAGE" --name "$CONTAINER" --auth-mode login >/dev/null
# The KEK may only wrap and unwrap data keys: it cannot encrypt, decrypt or sign.
az keyvault key create --vault-name "$KV" --name "$KEY_NAME" --kty RSA --size 3072 \
  --ops wrapKey unwrapKey >/dev/null

cat <<OUT

Done. In your .env, REMOVE AZURITE_BLOB_ENDPOINT and LOCAL_KEK, then add:
AZURE_STORAGE_ACCOUNT=$STORAGE
AZURE_STORAGE_CONTAINER=$CONTAINER
AZURE_KEYVAULT_URL=https://$KV.vault.azure.net/
AZURE_KEK_NAME=$KEY_NAME

The app signs in with your az login locally. When the app is deployed, grant
its Managed Identity the least-privilege roles (not the ones used above):
  az role assignment create --assignee <app-identity-id> --role "Storage Blob Data Contributor" --scope $STORAGE_ID/blobServices/default/containers/$CONTAINER
  az role assignment create --assignee <app-identity-id> --role "Key Vault Crypto User" --scope $KV_ID/keys/$KEY_NAME

To delete everything later:  az group delete --name $RG --yes
OUT
# Infrastructure Video Indexer

Ce dossier contient l'infrastructure Bicep du nouveau pipeline Smart Edit base sur Azure AI Video Indexer. Le pipeline Azure Content Understanding reste independant et n'est pas modifie par ce deploiement.

## Ce que le deploiement cree

`main.bicep` cree une stack dediee dans le resource group cible :

- un Container App `azure-video-indexer-service` ;
- une identite managée system-assigned pour le Container App ;
- un Storage Account avec les containers `video-indexer-staging` et `video-indexer-jobs` ;
- un workspace Log Analytics et une instance Application Insights ;
- les role assignments ACR Pull, Storage Blob Data Contributor, Foundry/OpenAI User et Video Indexer ;
- la configuration du conteneur, ses probes et son image ACR immuable.

Les comptes ACR, Foundry/Azure OpenAI, Video Indexer et l'environnement Container Apps sont des ressources existantes fournies en paramètres. Le template ne cree pas de nouveau compte de modele ni de compte Video Indexer.

## Prerequis Azure

Installer et authentifier Azure CLI :

```bash
az login
az account set --subscription "<SUBSCRIPTION_ID>"
az extension add --name containerapp --upgrade
az bicep install
```

Le deploiement doit utiliser une identite ayant :

- `Contributor` sur le resource group cible ;
- `User Access Administrator` ou `Owner` sur les scopes ACR, Storage, Foundry et Video Indexer, car Bicep cree des affectations RBAC ;
- acces de lecture aux ressources existantes passees au template.

Verifier avant le deploiement :

```bash
az group show --name "<RESOURCE_GROUP>"
az containerapp env show --name "<CONTAINER_APPS_ENV>" --resource-group "<RESOURCE_GROUP>"
az acr show --name "<ACR_NAME>" --resource-group "<ACR_RESOURCE_GROUP>"
az cognitiveservices account show --name "<FOUNDRY_ACCOUNT_NAME>" --resource-group "<FOUNDRY_ACCOUNT_RESOURCE_GROUP>"
az resource show --ids "<VIDEO_INDEXER_ACCOUNT_RESOURCE_ID>"
```

Le role Video Indexer n'est pas suppose universel entre tenants. Recuperer puis verifier son resource ID dans le tenant cible avant de renseigner `VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID` :

```bash
az role definition list \
  --custom-role-only false \
  --query "[?contains(to_string(roleName), 'Video Indexer')].{name:roleName,id:id}" \
  --output table
```

## Deploiement local avec Azure CLI

Le workflow GitHub construit d'abord l'image dans ACR puis deploye `infra/main.bicep`. Pour reproduire le deploiement localement, construire et pousser l'image :

```bash
az acr build \
  --registry "<ACR_NAME>" \
  --image "ai-video-indexer-service:local" \
  --file "azure-video-indexer-service/Dockerfile" \
  .
```

Resoudre l'ID de l'environnement Container Apps :

```bash
CONTAINER_APPS_ENVIRONMENT_ID="$(az containerapp env show \
  --name "<CONTAINER_APPS_ENV>" \
  --resource-group "<RESOURCE_GROUP>" \
  --query id --output tsv)"
```

Puis lancer la validation et le deploiement :

```bash
az deployment group validate \
  --resource-group "<RESOURCE_GROUP>" \
  --template-file infra/main.bicep \
  --parameters \
    location="<LOCATION>" \
    containerAppsEnvironmentId="$CONTAINER_APPS_ENVIRONMENT_ID" \
    containerRegistryName="<ACR_NAME>" \
    containerRegistryResourceGroupName="<ACR_RESOURCE_GROUP>" \
    foundryAccountName="<FOUNDRY_ACCOUNT_NAME>" \
    foundryProjectEndpoint="<FOUNDRY_PROJECT_ENDPOINT>" \
    foundryAccountResourceGroupName="<FOUNDRY_ACCOUNT_RESOURCE_GROUP>" \
    videoIndexerAccountName="<VIDEO_INDEXER_ACCOUNT_NAME>" \
    videoIndexerAccountResourceGroupName="<VIDEO_INDEXER_ACCOUNT_RESOURCE_GROUP>" \
    videoIndexerRoleDefinitionResourceId="<VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID>" \
    containerImageTag="local" \
    serviceApiKey="<SERVICE_API_KEY>"

az deployment group create \
  --resource-group "<RESOURCE_GROUP>" \
  --template-file infra/main.bicep \
  --parameters \
    location="<LOCATION>" \
    containerAppsEnvironmentId="$CONTAINER_APPS_ENVIRONMENT_ID" \
    containerRegistryName="<ACR_NAME>" \
    containerRegistryResourceGroupName="<ACR_RESOURCE_GROUP>" \
    foundryAccountName="<FOUNDRY_ACCOUNT_NAME>" \
    foundryProjectEndpoint="<FOUNDRY_PROJECT_ENDPOINT>" \
    foundryAccountResourceGroupName="<FOUNDRY_ACCOUNT_RESOURCE_GROUP>" \
    videoIndexerAccountName="<VIDEO_INDEXER_ACCOUNT_NAME>" \
    videoIndexerAccountResourceGroupName="<VIDEO_INDEXER_ACCOUNT_RESOURCE_GROUP>" \
    videoIndexerRoleDefinitionResourceId="<VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID>" \
    containerImageTag="local" \
    serviceApiKey="<SERVICE_API_KEY>"
```

Pour un deploiement CI/CD, utiliser de preference le workflow `Deploy azure-video-indexer-service` : il pousse une image taggee avec `GITHUB_SHA`, deploie la stack et teste `GET /ready`.

## Configurer les secrets et variables GitHub

Le workflow utilise l'environnement GitHub `production`. Dans **Settings > Environments > production**, creer les secrets et variables ci-dessous. Les noms sont sensibles a la casse.

### Secrets obligatoires

| Nom | Contenu | Comment l'obtenir ou le creer |
|---|---|---|
| `AZURE_CLIENT_ID` | Client ID de l'application Entra utilisee par GitHub OIDC | `az ad app show --id "<APP_ID_OR_URI>" --query appId -o tsv` |
| `AZURE_TENANT_ID` | ID du tenant Entra | `az account show --query tenantId -o tsv` |
| `AZURE_SUBSCRIPTION_ID` | ID de la souscription cible | `az account show --query id -o tsv` |
| `AZURE_VIDEO_INDEXER_API_KEY` | Cle aleatoire partagee entre le desktop et ce Container App | Generer une valeur aleatoire, par exemple `openssl rand -hex 32` ou `[Convert]::ToHexString((1..32 | ForEach-Object { Get-Random -Maximum 256 }))` dans PowerShell |

Cette cle n'est pas une cle Azure Video Indexer : elle protege l'API privee du nouveau service entre l'application desktop et le Container App. Elle doit aussi etre configuree dans les reglages desktop sous la forme de la meme valeur.

### Variables obligatoires

| Nom | Contenu | Exemple de forme |
|---|---|---|
| `AZURE_RESOURCE_GROUP` | Resource group qui recevra la nouvelle stack | `rg-ai-video-studio-prod` |
| `AZURE_LOCATION` | Region Azure de la stack | `westeurope` |
| `AZURE_CONTAINER_APPS_ENV` | Nom de l'environnement Container Apps existant | `cae-ai-video-studio` |
| `ACR_NAME` | Nom de l'ACR existant | `acrvideostudio` |
| `FOUNDRY_ACCOUNT_NAME` | Nom du compte Foundry/Azure OpenAI existant | `oai-video-studio` |
| `FOUNDRY_PROJECT_ENDPOINT` | Endpoint du projet Foundry, pas l'endpoint generique du compte | `https://<resource>.services.ai.azure.com/api/projects/<project>` |
| `VIDEO_INDEXER_ACCOUNT_NAME` | Nom du compte Azure AI Video Indexer existant | `videoindexer-prod` |
| `VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID` | Resource ID du role Video Indexer verifie dans le tenant | `/subscriptions/<id>/providers/Microsoft.Authorization/roleDefinitions/<guid>` |

### Variables optionnelles

Ces variables ne sont necessaires que si les ressources existantes sont dans d'autres resource groups ou souscriptions :

| Nom | Valeur par defaut |
|---|---|
| `ACR_RESOURCE_GROUP` | `AZURE_RESOURCE_GROUP` |
| `FOUNDRY_ACCOUNT_RESOURCE_GROUP` | `AZURE_RESOURCE_GROUP` |
| `FOUNDRY_ACCOUNT_SUBSCRIPTION_ID` | `AZURE_SUBSCRIPTION_ID` |
| `VIDEO_INDEXER_ACCOUNT_RESOURCE_GROUP` | `AZURE_RESOURCE_GROUP` |
| `VIDEO_INDEXER_ACCOUNT_SUBSCRIPTION_ID` | `AZURE_SUBSCRIPTION_ID` |

Le workflow valide automatiquement les valeurs obligatoires avant le login Azure et echoue si l'une d'elles est vide.

## Creer l'identite OIDC GitHub

Creer une application Entra dediee au repository, puis une identite federated credential limitee a la branche `main` :

```bash
az ad app create --display-name "github-ai-video-studio-deploy"
APP_ID="$(az ad app list --display-name "github-ai-video-studio-deploy" --query '[0].appId' -o tsv)"
az ad sp create --id "$APP_ID"
```

Creer ensuite une federated credential dans l'application avec :

- issuer : `https://token.actions.githubusercontent.com` ;
- subject : `repo:<OWNER>/<REPO>:ref:refs/heads/main` ;
- audience : `api://AzureADTokenExchange`.

Avec Azure CLI, la credential peut etre creee ainsi :

```bash
az ad app federated-credential create \
  --id "$APP_ID" \
  --parameters '{"name":"github-main","issuer":"https://token.actions.githubusercontent.com","subject":"repo:<OWNER>/<REPO>:ref:refs/heads/main","audiences":["api://AzureADTokenExchange"]}'
```

L'application doit recevoir `Contributor` sur le resource group cible et `User Access Administrator` (ou `Owner`) sur les scopes où les modules Bicep creent des role assignments. Utiliser des permissions plus larges uniquement si la topologie de ressources l'exige.

Afficher les identifiants a reporter dans les secrets :

```bash
echo "AZURE_CLIENT_ID=$APP_ID"
az account show --query '{tenantId:tenantId,subscriptionId:id}' -o json
```

Ne jamais commiter ces valeurs dans le depot lorsqu'elles sont associees a des credentials, tokens ou cles. Les valeurs non secretes peuvent rester dans les GitHub Variables ; les cles et tokens vont exclusivement dans GitHub Secrets.

### Enregistrer les valeurs avec GitHub CLI

Depuis la racine du depot, apres avoir execute `gh auth login` et selectionne le bon repository, utiliser l'environnement `production` :

```bash
gh secret set AZURE_CLIENT_ID --env production --body "$APP_ID"
gh secret set AZURE_TENANT_ID --env production --body "$(az account show --query tenantId -o tsv)"
gh secret set AZURE_SUBSCRIPTION_ID --env production --body "$(az account show --query id -o tsv)"

# Ne pas mettre la cle dans la ligne de commande ou dans l'historique shell.
read -r -s -p "Service API key: " SERVICE_API_KEY; echo
gh secret set AZURE_VIDEO_INDEXER_API_KEY --env production --body "$SERVICE_API_KEY"
unset SERVICE_API_KEY

gh variable set AZURE_RESOURCE_GROUP --env production --body "<RESOURCE_GROUP>"
gh variable set AZURE_LOCATION --env production --body "<LOCATION>"
gh variable set AZURE_CONTAINER_APPS_ENV --env production --body "<CONTAINER_APPS_ENV>"
gh variable set ACR_NAME --env production --body "<ACR_NAME>"
gh variable set FOUNDRY_ACCOUNT_NAME --env production --body "<FOUNDRY_ACCOUNT_NAME>"
gh variable set FOUNDRY_PROJECT_ENDPOINT --env production --body "<FOUNDRY_PROJECT_ENDPOINT>"
gh variable set VIDEO_INDEXER_ACCOUNT_NAME --env production --body "<VIDEO_INDEXER_ACCOUNT_NAME>"
gh variable set VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID --env production --body "<VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID>"
```

Ajouter aussi les variables optionnelles avec `gh variable set` si l'ACR, Foundry ou Video Indexer est dans un autre resource group ou une autre souscription. Pour eviter de recopier une valeur sensible, ne pas utiliser `--body` avec un token ou une cle dans un script versionne.

## Verification apres deploiement

Le workflow effectue deja le smoke test `/ready`. Pour verifier manuellement :

```bash
FQDN="$(az containerapp show \
  --name azure-video-indexer-service \
  --resource-group "<RESOURCE_GROUP>" \
  --query properties.configuration.ingress.fqdn -o tsv)"
curl --fail "https://${FQDN}/health"
curl --fail "https://${FQDN}/ready"
```

`/health` est une liveness legere. `/ready` verifie aussi Storage, la delegation SAS, Video Indexer, Foundry et `ffmpeg`/`ffprobe`. Un retour `503` indique qu'une dependance ou une permission de runtime n'est pas prete.

## Depannage courant

- **`AuthorizationFailed` pendant Bicep** : l'identite OIDC n'a pas `Contributor` ou `User Access Administrator` sur l'un des scopes.
- **`Unable to resolve Container Apps environment ID`** : le nom ou le resource group de l'environnement est incorrect.
- **Echec du role Video Indexer** : le resource ID du role est incorrect pour le tenant ; verifier le role dans Azure avant de relancer.
- **`/ready` retourne `503`** : consulter les logs du Container App et verifier les role assignments de l'identite system-assigned, le endpoint Foundry projet et le deploiement `gpt-5.4`.
- **Smoke test en timeout** : verifier l'ingress HTTPS, le quota Container Apps et les logs Application Insights. Ne pas rendre `/ready` public avec une cle dans l'URL.

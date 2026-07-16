# Infrastructure Video Indexer

Ce dossier contient l'infrastructure Bicep du nouveau pipeline Smart Edit base sur Azure AI Video Indexer. Le pipeline Azure Content Understanding reste independant et n'est pas modifie par ce deploiement.

## Architecture Durable Task Scheduler

`main.bicep` deploys the Video Indexer pipeline as two separate Container Apps backed by Azure Durable Task Scheduler (DTS):

- `azure-video-indexer-api` exposes the authenticated HTTP API, streams OneDrive sources into Blob staging, writes the public `JobDocument` projection, and schedules DTS orchestration instances;
- `azure-video-indexer-worker` has no ingress and runs the durable orchestration and short activities for Video Indexer, normalization, Foundry planning, timeline generation, cleanup, and cancellation compensation;
- a serverless/Consumption DTS scheduler and task hub are provisioned in the deployment region with the Container Apps environment and Storage account;
- API and worker have separate user-assigned identities. Both use `minReplicas: 0`; the API uses HTTP scaling and the worker uses `azure-durabletask-scheduler` activity and orchestration scaler rules.

The original OneDrive delegated token is used only while the API synchronously stages the source Blob. It is not included in DTS input, history, output, status, or logs. Blob `JobDocument` remains the public source of truth for GET/list status endpoints; DTS is the execution engine. Azure Content Understanding remains independent and is not modified by this deployment.

The worker depends on the experimental `durabletask-go` DTS backend pinned to immutable commit `9fa0fcd1a58ca379c0257c0b21ec9ce04df11795` from PR #122. Do not upgrade it automatically: validate the backend and scaler APIs in a non-production subscription before moving to a released compatible version.

## Ce que le deploiement cree

`main.bicep` creates a dedicated stack in the target resource group:

- API and worker Container Apps using the same immutable image with `SERVICE_ROLE=api` and `SERVICE_ROLE=worker`;
- a serverless DTS scheduler and task hub, the two user-assigned identities, and their scoped RBAC assignments;
- a Storage Account with `video-indexer-staging` and `video-indexer-jobs` containers;
- an Azure AI Video Indexer account with a system-assigned identity, connected to the same Standard StorageV2 account used for staging and jobs by the Container Apps and to a Foundry account provisioned in this resource group;
- a Foundry account, project, and `gpt-5.4` model deployment provisioned in the target resource group;
- Log Analytics and Application Insights;
- an Azure Container Registry Basic dans le resource group cible ;
- ACR Pull, Storage Blob Data Contributor, Foundry/OpenAI User, Video Indexer, and the built-in `Durable Task Data Contributor` role assignments scoped to the DTS scheduler. Bicep grants the Video Indexer system identity `Storage Blob Data Contributor` on the shared application storage account and `Cognitive Services OpenAI User` on the connected Foundry/Azure OpenAI account.

Only the API has ingress. The worker is started by DTS work items and must retain both scaler rules for scale-from-zero. The built-in `Durable Task Data Contributor` role (`0ad04412-c4d5-4796-b79c-f76d14c8d402`) is assigned by Bicep at scheduler scope; no GitHub variable is required.
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
- `User Access Administrator` ou `Owner` sur le resource group cible, car Bicep cree des affectations RBAC ;
- Acces de lecture aux ressources existantes passees au template. Le compte Video Indexer est cree dans le resource group cible, utilise le Storage Account de la stack et est connecte au compte Foundry/Azure OpenAI cree par le meme template.

Verifier avant le deploiement :

```bash
az group show --name "<RESOURCE_GROUP>"
az containerapp env show --name "<CONTAINER_APPS_ENV>" --resource-group "<RESOURCE_GROUP>"
az acr show --name "<ACR_NAME>" --resource-group "<RESOURCE_GROUP>"
az cognitiveservices account show --name "<FOUNDRY_ACCOUNT_NAME>" --resource-group "<RESOURCE_GROUP>"
```

Le compte Video Indexer est cree par `main.bicep`. Verifier son existence apres le deploiement avec :

```bash
az resource show \
  --resource-group "<RESOURCE_GROUP>" \
  --resource-type Microsoft.VideoIndexer/accounts \
  --name "<VIDEO_INDEXER_ACCOUNT_NAME>"
```

Le role Video Indexer n'est pas suppose universel entre tenants. Recuperer puis verifier son resource ID dans le tenant cible avant de renseigner `VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID` :

```bash
az role definition list \
  --custom-role-only false \
  --query "[?contains(to_string(roleName), 'Video Indexer')].{name:roleName,id:id}" \
  --output table
```

### Connexion Video Indexer et Azure OpenAI

`main.bicep` cree un compte Foundry `AIServices`, un projet et le deploiement de modele `gpt-5.4` (version Azure `2026-03-05`) dans le resource group cible. Il cree aussi un compte Azure OpenAI dedie (`videoIndexerOpenAIAccountName`) pour la connexion native Video Indexer : l'API `Microsoft.VideoIndexer/accounts@2025-04-01` le relie au compte Video Indexer avec l'identite system-assigned de ce dernier, sans cle OpenAI. Bicep attribue `Cognitive Services OpenAI User` a cette identite sur le compte Azure OpenAI.

Cette connexion active les capacites natives Video Indexer qui utilisent Azure OpenAI. Elle ne remplace pas l'acces du worker : celui-ci conserve sa propre affectation `Cognitive Services OpenAI User` et utilise `FOUNDRY_PROJECT_ENDPOINT` avec `FOUNDRY_DEPLOYMENT_NAME` pour le planning d'edition. Microsoft recommande de placer Video Indexer et Azure OpenAI dans la meme region.

## Workflows GitHub Actions

Trois workflows independants gerent le pipeline Video Indexer :

- `Deploy azure-video-indexer-service` est le workflow de bootstrap et d'infrastructure. Il est declenche sur les changements dans `infra/**`, `azure.yaml` ou son propre fichier. Il cree ou met a jour l'ACR et la stack Bicep, construit les deux images, puis deploie les Container Apps. Utiliser ce workflow pour le premier deploiement et tout changement d'infrastructure.
- `Deploy Video Indexer API image` est declenche sur les changements du service API/durable worker, du front-end, des dependances ou de son propre fichier. Il teste le service, publie l'image API avec le tag immuable `GITHUB_SHA`, puis met a jour `video-indexer-api` et `video-indexer-worker` sans redeployer Bicep.
- `Deploy Video Indexer FFmpeg image` est declenche par le Dockerfile FFmpeg et le code de rendu associe. Il publie l'image avec le tag immuable `GITHUB_SHA`, puis met a jour uniquement `ffmpeg-render-worker`. Les changements du code Go commun et de `internal/**` declenchent les deux workflows d'images car les deux images compilent ce code.

Les workflows d'images exigent que l'ACR et les Container Apps concernes existent deja. Ils echouent avec une erreur actionnable lorsqu'ils sont absents : lancer alors le workflow infrastructure. Pour un changement qui modifie a la fois l'infrastructure et le runtime, lancer le workflow infrastructure en premier ; relancer ensuite le ou les workflows d'images si une nouvelle image est requise.

Les trois workflows utilisent l'environnement GitHub `production`, les memes secrets OIDC et les variables `AZURE_RESOURCE_GROUP` et `ACR_NAME`. Le workflow infrastructure utilise en plus les variables Foundry et Video Indexer decrites ci-dessous.

## Deploiement local avec Azure CLI

Le workflow GitHub cree d'abord l'ACR avec `infra/container-registry.bicep`, construit ensuite l'image, puis deploye `infra/main.bicep`. Pour reproduire le deploiement localement, creer d'abord l'ACR :

```bash
az deployment group create \
  --name "bootstrap-container-registry" \
  --resource-group "<RESOURCE_GROUP>" \
  --template-file infra/container-registry.bicep \
  --parameters location="<LOCATION>" containerRegistryName="<ACR_NAME>"
```

Construire ensuite l'image :

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
    foundryAccountName="<FOUNDRY_ACCOUNT_NAME>" \
    foundryProjectName="video-indexer-project" \
    videoIndexerAccountName="<VIDEO_INDEXER_ACCOUNT_NAME>" \
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
    foundryAccountName="<FOUNDRY_ACCOUNT_NAME>" \
    foundryProjectName="video-indexer-project" \
    videoIndexerAccountName="<VIDEO_INDEXER_ACCOUNT_NAME>" \
    videoIndexerRoleDefinitionResourceId="<VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID>" \
    containerImageTag="local" \
    serviceApiKey="<SERVICE_API_KEY>"
```

Pour le premier deploiement CI/CD ou une modification Bicep, utiliser `Deploy azure-video-indexer-service`. Pour une modification applicative seulement, utiliser `Deploy Video Indexer API image` ou `Deploy Video Indexer FFmpeg image` selon le composant modifie ; chaque workflow pousse une image taggee avec `GITHUB_SHA` et met a jour uniquement ses Container Apps.

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
| `ACR_NAME` | Nom facultatif de l'ACR cree par Bicep dans le resource group cible. Si la variable n'est pas definie, le workflow et Bicep utilisent `acrvideostudio`. | `acrvideostudio` |
| `FOUNDRY_ACCOUNT_NAME` | Nom du compte Foundry/Azure OpenAI cree par Bicep dans le resource group cible. Il doit etre globalement disponible comme nom de sous-domaine. | `aivideoindexerfoundry` |
| `FOUNDRY_PROJECT_NAME` | Nom du projet Foundry cree par Bicep | `video-indexer-project` |
| `VIDEO_INDEXER_ACCOUNT_NAME` | Nom facultatif du compte Azure AI Video Indexer a creer dans le resource group cible. Si la variable n'est pas definie, le workflow et Bicep utilisent `videoindexer-prod`. | `videoindexer-prod` |
| `VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID` | Variable facultative permettant de forcer le Resource ID du role Video Indexer. Si elle est absente, le workflow le recherche apres la connexion Azure et echoue si la recherche ne renvoie pas exactement un role. | `/subscriptions/<id>/providers/Microsoft.Authorization/roleDefinitions/<guid>` |

Le workflow valide automatiquement les valeurs obligatoires avant le login Azure et echoue si l'une d'elles est vide.

## Creer l'identite OIDC GitHub

Creer une application Entra dediee au repository, puis une federated credential limitee a l'environnement GitHub `production`. Le workflow utilise cet environnement, donc le sujet OIDC n'est pas celui d'une branche :

```bash
az ad app create --display-name "github-ai-video-studio-deploy"
APP_ID="$(az ad app list --display-name "github-ai-video-studio-deploy" --query '[0].appId' -o tsv)"
az ad sp create --id "$APP_ID"
```

Creer ensuite une federated credential dans l'application avec :

- issuer : `https://token.actions.githubusercontent.com` ;
- subject : `repo:<OWNER>/<REPO>:environment:production` ;
- audience : `api://AzureADTokenExchange`.

Avec Azure CLI, la credential peut etre creee ainsi :

```bash
az ad app federated-credential create \
  --id "$APP_ID" \
  --parameters '{"name":"github-production","issuer":"https://token.actions.githubusercontent.com","subject":"repo:<OWNER>/<REPO>:environment:production","audiences":["api://AzureADTokenExchange"]}'
```

Pour ce depot, le sujet attendu est `repo:zecloud/ai-video-studio:environment:production`. Il doit correspondre exactement au sujet de la federated credential, avec le meme issuer et la meme audience. Une erreur `AADSTS700213` indique que cette credential est absente ou ne correspond pas a ces valeurs.

L'application doit recevoir `Contributor` sur le resource group cible et `User Access Administrator` (ou `Owner`) sur les scopes où les modules Bicep creent des role assignments. Utiliser des permissions plus larges uniquement si la topologie de ressources l'exige.

## Attribuer les permissions a l'application OIDC

Cette procedure s'execute depuis **Azure Cloud Shell en Bash/Linux** avant le premier lancement du workflow. L'identite a laquelle les roles sont attribues est le service principal correspondant a `AZURE_CLIENT_ID` (et non l'identite geree par les Container Apps). Les commandes qui suivent doivent etre lancees par une identite disposant deja de `Owner` ou de `User Access Administrator` sur les scopes concernes, car `Microsoft.Authorization/roleAssignments/write` est necessaire.

Renseigner les variables avec les IDs reels de la topologie. Ne pas mettre de secret dans ces variables :

```bash
AZURE_CLIENT_ID="<AZURE_CLIENT_ID>"
TARGET_SUBSCRIPTION_ID="<TARGET_SUBSCRIPTION_ID>"
TARGET_RESOURCE_GROUP="<TARGET_RESOURCE_GROUP>"
FOUNDRY_SUBSCRIPTION_ID="<FOUNDRY_SUBSCRIPTION_ID>"
FOUNDRY_RESOURCE_GROUP="<FOUNDRY_RESOURCE_GROUP>"
```

Recuperer l'object ID du service principal avec `az ad sp show --id`, puis verifier que la valeur est bien un GUID non vide :

```bash
SP_OBJECT_ID="$(az ad sp show \
  --id "$AZURE_CLIENT_ID" \
  --query id \
  --output tsv)"

if [[ ! "$SP_OBJECT_ID" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]; then
  echo "Object ID du service principal introuvable pour AZURE_CLIENT_ID" >&2
  exit 1
fi
echo "Service principal: $SP_OBJECT_ID"
```

Attribuer `Contributor` au service principal sur le resource group qui recoit la stack :

```bash
TARGET_RG_SCOPE="/subscriptions/$TARGET_SUBSCRIPTION_ID/resourceGroups/$TARGET_RESOURCE_GROUP"

az role assignment create \
  --assignee-object-id "$SP_OBJECT_ID" \
  --assignee-principal-type ServicePrincipal \
  --role "Contributor" \
  --scope "$TARGET_RG_SCOPE"
```

Attribuer `User Access Administrator` sur chaque resource group ou scope dans lequel `infra/main.bicep` cree une affectation de role. Le scope du resource group cible couvre les affectations ACR Pull, Storage Blob Data Contributor, Durable Task Data Contributor, le Storage du Video Indexer et le role Video Indexer. L'autre scope couvre l'affectation Foundry/OpenAI :

```bash
FOUNDRY_RG_SCOPE="/subscriptions/$FOUNDRY_SUBSCRIPTION_ID/resourceGroups/$FOUNDRY_RESOURCE_GROUP"

for SCOPE in \
  "$TARGET_RG_SCOPE" \
  "$FOUNDRY_RG_SCOPE"; do
  az role assignment create \
    --assignee-object-id "$SP_OBJECT_ID" \
    --assignee-principal-type ServicePrincipal \
    --role "User Access Administrator" \
    --scope "$SCOPE"
done
```

Si Foundry se trouve dans un autre resource group ou une autre souscription, conserver ses IDs correspondants dans les variables ci-dessus et executer les commandes sur ce scope. L'ACR, le compte Video Indexer et le Storage Account sont crees dans le resource group cible. `Contributor` sur un resource group externe n'est necessaire que si le workflow doit aussi y modifier des ressources ; pour les affectations RBAC creees par Bicep, `User Access Administrator` sur le scope suffit. Si une ressource externe est seulement lue ou utilisee comme dependance, ne pas accorder de `Contributor` inutilement.

Verifier les affectations de l'identite et leurs scopes avant de lancer le workflow :

```bash
az role assignment list \
  --assignee-object-id "$SP_OBJECT_ID" \
  --all \
  --include-inherited \
  --query "[].{role:roleDefinitionName,scope:scope,principalId:principalId}" \
  --output table
```

La verification doit notamment montrer `Contributor` sur `$TARGET_RG_SCOPE` et `User Access Administrator` sur chacun des scopes qui contient une affectation Bicep. Ces permissions permettent au deploiement GitHub Actions OIDC de creer le compte Video Indexer et son stockage, ainsi que les roles `ACR Pull`, `Storage Blob Data Contributor`, `Foundry/OpenAI`, `Video Indexer` et `Durable Task Data Contributor` pour les identites gerees deployees par `main.bicep`; elles ne modifient pas le workflow et ne remplacent pas les permissions runtime de ces identites.

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
# Facultatif : omettre cette variable pour utiliser la valeur par defaut.
gh variable set ACR_NAME --env production --body "<ACR_NAME>"
gh variable set FOUNDRY_ACCOUNT_NAME --env production --body "<FOUNDRY_ACCOUNT_NAME>"
gh variable set FOUNDRY_PROJECT_NAME --env production --body "video-indexer-project"
# Facultatif : omettre cette variable pour utiliser la valeur par defaut.
gh variable set VIDEO_INDEXER_ACCOUNT_NAME --env production --body "<VIDEO_INDEXER_ACCOUNT_NAME>"
# Facultatif : le workflow peut rechercher automatiquement l'unique role contenant
# "Video Indexer" apres Azure Login. Definir cette variable seulement pour forcer un ID.
gh variable set VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID --env production --body "<VIDEO_INDEXER_ROLE_DEFINITION_RESOURCE_ID>"
```

Ajouter aussi les variables optionnelles avec `gh variable set` si Foundry est dans un autre resource group ou une autre souscription. Pour eviter de recopier une valeur sensible, ne pas utiliser `--body` avec un token ou une cle dans un script versionne.

## Verification apres deploiement

Le workflow effectue deja le smoke test `/ready`. Pour verifier manuellement :

```bash
FQDN="$(az containerapp show \
  --name azure-video-indexer-service-api \
  --resource-group "<RESOURCE_GROUP>" \
  --query properties.configuration.ingress.fqdn -o tsv)"
curl --fail "https://${FQDN}/health"
curl --fail "https://${FQDN}/ready"
```

`/health` est une liveness legere. `/ready` de l'API verifie sa configuration et les containers Storage; le worker n'a pas d'ingress et ne doit pas etre teste via HTTP. Un retour `503` indique qu'une dependance ou une permission de runtime n'est pas prete.

## Depannage courant

- **`AuthorizationFailed` pendant Bicep** : l'identite OIDC n'a pas `Contributor` ou `User Access Administrator` sur l'un des scopes.
- **`Unable to resolve Container Apps environment ID`** : le nom ou le resource group de l'environnement est incorrect.
- **Echec du role Video Indexer** : le resource ID du role est incorrect pour le tenant ; verifier le role dans Azure avant de relancer.
- **`/ready` retourne `503`** : consulter les logs du Container App et verifier les role assignments des identites gerees assignees par l'utilisateur, le endpoint Foundry projet et le deploiement `gpt-5.4`.
- **Smoke test en timeout** : verifier l'ingress HTTPS, le quota Container Apps et les logs Application Insights. Ne pas rendre `/ready` public avec une cle dans l'URL.

## DTS identity and network boundary

Both apps use separate user-assigned managed identities. The deployment sets each app's `AZURE_CLIENT_ID`, which is honored by `DefaultAzureCredential` in the immutable experimental `durabletask-go` dependency pinned by this service. This selects the corresponding UAMI for Storage and DTS without putting credentials in DTS inputs.

The DTS API requires a non-empty IP allowlist. `main.bicep` currently uses `0.0.0.0/0` because Container Apps has no stable outbound IP in this topology. DTS access is still protected by Entra authentication and the scheduler-scoped `Durable Task Data Contributor` role. Restrict this list only after validating a stable, supported Container Apps egress design in the target environment.

## Scale-to-zero validation

After a validation deployment, confirm both apps are at zero replicas, then send an authenticated `POST /api/v1/index-jobs` using a non-sensitive test OneDrive fixture. Confirm the API wakes for staging, DTS schedules the orchestration, and both worker scaler rules wake the worker for orchestration and activity work items. Verify the Blob `JobDocument` reaches a terminal state and both apps return to zero after draining. This repository does not deploy a fallback execution backend.
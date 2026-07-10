targetScope = 'resourceGroup'

@description('Azure region for all resources in this stack.')
param location string = resourceGroup().location

@description('Container Apps resource environment ID to reuse.')
param containerAppsEnvironmentId string

@description('Azure Container Registry name.')
param containerRegistryName string

@description('Azure Container Registry resource group name.')
param containerRegistryResourceGroupName string = resourceGroup().name

@description('Azure Container Registry subscription ID.')
param containerRegistrySubscriptionId string = subscription().subscriptionId

@description('Azure AI Foundry / Azure OpenAI account name.')
param foundryAccountName string

@description('Azure AI Foundry / Azure OpenAI account resource group name.')
param foundryAccountResourceGroupName string

@description('Azure AI Foundry / Azure OpenAI account subscription ID.')
param foundryAccountSubscriptionId string = subscription().subscriptionId

@description('Azure AI Foundry project endpoint used by the edit planner, e.g. https://<resource>.services.ai.azure.com/api/projects/<project>.')
param foundryProjectEndpoint string

@description('Azure AI Video Indexer account name.')
param videoIndexerAccountName string

@description('Azure AI Video Indexer account resource group name.')
param videoIndexerAccountResourceGroupName string

@description('Azure AI Video Indexer account subscription ID.')
param videoIndexerAccountSubscriptionId string = subscription().subscriptionId

@description('Existing Azure AI Video Indexer role definition resource ID. Leave as a verified value from the target tenant.')
param videoIndexerRoleDefinitionResourceId string

@description('Azure AI deployment name used by the service.')
param foundryDeploymentName string = 'gpt-5.4'

@secure()
@description('Service API key consumed by the container app.')
param serviceApiKey string

@description('Container image repository name in the linked ACR.')
param containerImageRepository string = 'ai-video-indexer-service'

@description('Container image tag in the linked ACR.')
param containerImageTag string = 'latest'

@description('Dedicated storage account name for the new service.')
param storageAccountName string = toLower('st${uniqueString(resourceGroup().id, 'azure-video-indexer-service')}')

@description('Log Analytics workspace name for this service.')
param logAnalyticsWorkspaceName string = toLower('law-${uniqueString(resourceGroup().id, 'azure-video-indexer-service')}')

@description('Application Insights component name for this service.')
param appInsightsName string = toLower('appi-${uniqueString(resourceGroup().id, 'azure-video-indexer-service')}')

var serviceName = 'azure-video-indexer-service'
var stagingContainerName = 'video-indexer-staging'
var jobsContainerName = 'video-indexer-jobs'
var acrPullRoleDefinitionId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'
var storageBlobDataContributorRoleDefinitionId = 'ba92f5b4-2d11-453d-a403-e96b0029c9fe'
var cognitiveServicesOpenAIUserRoleDefinitionId = '5e0bd9bd-7b93-4f28-af87-19fc36ad61bd'

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: containerRegistryName
  scope: resourceGroup(containerRegistrySubscriptionId, containerRegistryResourceGroupName)
}

resource foundryAccount 'Microsoft.CognitiveServices/accounts@2023-05-01' existing = {
  name: foundryAccountName
  scope: resourceGroup(foundryAccountSubscriptionId, foundryAccountResourceGroupName)
}

resource videoIndexerAccount 'Microsoft.VideoIndexer/accounts@2024-01-01' existing = {
  name: videoIndexerAccountName
  scope: resourceGroup(videoIndexerAccountSubscriptionId, videoIndexerAccountResourceGroupName)
}

resource logAnalytics 'Microsoft.OperationalInsights/workspaces@2023-09-01' = {
  name: logAnalyticsWorkspaceName
  location: location
  properties: {
    sku: {
      name: 'PerGB2018'
    }
    retentionInDays: 30
  }
}

resource appInsights 'Microsoft.Insights/components@2020-02-02' = {
  name: appInsightsName
  location: location
  kind: 'web'
  properties: {
    Application_Type: 'web'
    WorkspaceResourceId: logAnalytics.id
  }
}

resource storageAccount 'Microsoft.Storage/storageAccounts@2023-05-01' = {
  name: storageAccountName
  location: location
  kind: 'StorageV2'
  sku: {
    name: 'Standard_LRS'
  }
  properties: {
    allowBlobPublicAccess: false
    allowSharedKeyAccess: false
    minimumTlsVersion: 'TLS1_2'
    publicNetworkAccess: 'Enabled'
    supportsHttpsTrafficOnly: true
  }
}

resource storageBlobService 'Microsoft.Storage/storageAccounts/blobServices@2023-05-01' = {
  parent: storageAccount
  name: 'default'
}

resource stagingContainer 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: storageBlobService
  name: stagingContainerName
  properties: {
    publicAccess: 'None'
  }
}

resource jobsContainer 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: storageBlobService
  name: jobsContainerName
  properties: {
    publicAccess: 'None'
  }
}

resource containerApp 'Microsoft.App/containerApps@2024-03-01' = {
  name: serviceName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    environmentId: containerAppsEnvironmentId
    configuration: {
      activeRevisionsMode: 'Single'
      ingress: {
        external: true
        allowInsecure: false
        targetPort: 8080
        transport: 'auto'
      }
      registries: [
        {
          server: acr.properties.loginServer
          identity: 'system'
        }
      ]
      secrets: [
        {
          name: 'appinsights-connection-string'
          value: appInsights.properties.ConnectionString
        }
        {
          name: 'service-api-key'
          value: serviceApiKey
        }
      ]
    }
    template: {
      containers: [
        {
          name: serviceName
          image: '${acr.properties.loginServer}/${containerImageRepository}:${containerImageTag}'
          env: [
            {
              name: 'APPLICATIONINSIGHTS_CONNECTION_STRING'
              secretRef: 'appinsights-connection-string'
            }
            {
              name: 'API_KEY'
              secretRef: 'service-api-key'
            }
            {
              name: 'AZURE_STORAGE_ACCOUNT_NAME'
              value: storageAccount.name
            }
            {
              name: 'AZURE_STORAGE_URL'
              value: storageAccount.properties.primaryEndpoints.blob
            }
            {
              name: 'AZURE_STORAGE_BLOB_ENDPOINT'
              value: storageAccount.properties.primaryEndpoints.blob
            }
            {
              name: 'AZURE_STORAGE_STAGING_CONTAINER'
              value: stagingContainerName
            }
            {
              name: 'AZURE_STORAGE_JOBS_CONTAINER'
              value: jobsContainerName
            }
            {
              name: 'AZURE_VIDEO_INDEXER_SUBSCRIPTION_ID'
              value: videoIndexerAccountSubscriptionId
            }
            {
              name: 'AZURE_VIDEO_INDEXER_RESOURCE_GROUP'
              value: videoIndexerAccountResourceGroupName
            }
            {
              name: 'AZURE_VIDEO_INDEXER_ACCOUNT_NAME'
              value: videoIndexerAccount.name
            }
            {
              name: 'AZURE_VIDEO_INDEXER_ACCOUNT_ID'
              value: videoIndexerAccount.properties.accountId
            }
            {
              name: 'AZURE_VIDEO_INDEXER_ACCOUNT_RESOURCE_ID'
              value: videoIndexerAccount.id
            }
            {
              name: 'AZURE_VIDEO_INDEXER_LOCATION'
              value: videoIndexerAccount.location
            }
            {
              name: 'AZURE_FOUNDRY_ACCOUNT_NAME'
              value: foundryAccount.name
            }
            {
              name: 'AZURE_FOUNDRY_ACCOUNT_RESOURCE_ID'
              value: foundryAccount.id
            }
            {
              name: 'AZURE_FOUNDRY_ENDPOINT'
              value: foundryProjectEndpoint
            }
            {
              name: 'FOUNDRY_ENDPOINT'
              value: foundryProjectEndpoint
            }
            {
              name: 'AZURE_OPENAI_ENDPOINT'
              value: foundryAccount.properties.endpoint
            }
            {
              name: 'AZURE_FOUNDRY_DEPLOYMENT_NAME'
              value: foundryDeploymentName
            }
            {
              name: 'FOUNDRY_DEPLOYMENT_NAME'
              value: foundryDeploymentName
            }
            {
              name: 'AZURE_OPENAI_DEPLOYMENT_NAME'
              value: foundryDeploymentName
            }
            {
              name: 'LISTEN_ADDR'
              value: ':8080'
            }
            {
              name: 'OTEL_SERVICE_NAME'
              value: serviceName
            }
          ]
          resources: {
            cpu: json('0.5')
            memory: '1Gi'
          }
          probes: [
            {
              type: 'startup'
              httpGet: {
                path: '/health'
                port: 8080
              }
              initialDelaySeconds: 0
              periodSeconds: 10
              failureThreshold: 30
            }
            {
              type: 'readiness'
              httpGet: {
                path: '/ready'
                port: 8080
              }
              initialDelaySeconds: 5
              periodSeconds: 10
              timeoutSeconds: 10
              failureThreshold: 3
            }
            {
              type: 'liveness'
              httpGet: {
                path: '/health'
                port: 8080
              }
              initialDelaySeconds: 10
              periodSeconds: 30
              failureThreshold: 3
            }
          ]
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 1
      }
    }
  }
}

module containerAppAcrPullRoleAssignment 'acr-role-assignment.bicep' = {
  name: 'container-app-acr-pull'
  scope: resourceGroup(containerRegistrySubscriptionId, containerRegistryResourceGroupName)
  params: {
    registryName: containerRegistryName
    principalId: containerApp.identity.principalId
    roleDefinitionId: acrPullRoleDefinitionId
    assignmentSeed: serviceName
  }
}

resource containerAppStorageRoleAssignment 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(storageAccount.id, serviceName, storageBlobDataContributorRoleDefinitionId)
  scope: storageAccount
  properties: {
    principalId: containerApp.identity.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', storageBlobDataContributorRoleDefinitionId)
  }
}

module containerAppFoundryRoleAssignment 'foundry-role-assignment.bicep' = {
  name: 'container-app-foundry-user'
  scope: resourceGroup(foundryAccountSubscriptionId, foundryAccountResourceGroupName)
  params: {
    accountName: foundryAccountName
    principalId: containerApp.identity.principalId
    roleDefinitionId: cognitiveServicesOpenAIUserRoleDefinitionId
    assignmentSeed: serviceName
  }
}

module containerAppVideoIndexerRoleAssignment 'video-indexer-role-assignment.bicep' = {
  name: 'container-app-video-indexer'
  scope: resourceGroup(videoIndexerAccountSubscriptionId, videoIndexerAccountResourceGroupName)
  params: {
    accountName: videoIndexerAccountName
    principalId: containerApp.identity.principalId
    roleDefinitionResourceId: videoIndexerRoleDefinitionResourceId
    assignmentSeed: serviceName
  }
}

output containerAppName string = containerApp.name
output containerAppFqdn string = containerApp.properties.configuration.ingress.fqdn
output containerAppIdentityPrincipalId string = containerApp.identity.principalId

output storageAccountResourceId string = storageAccount.id
output storageAccountName string = storageAccount.name
output storageAccountBlobEndpoint string = storageAccount.properties.primaryEndpoints.blob
output storageStagingContainerName string = stagingContainer.name
output storageJobsContainerName string = jobsContainer.name

output appInsightsResourceId string = appInsights.id
output logAnalyticsWorkspaceResourceId string = logAnalytics.id

output foundryAccountResourceId string = foundryAccount.id
output foundryAccountName string = foundryAccount.name
output foundryEndpoint string = foundryAccount.properties.endpoint
output foundryDeploymentName string = foundryDeploymentName

output videoIndexerAccountResourceId string = videoIndexerAccount.id
output videoIndexerAccountName string = videoIndexerAccount.name
output videoIndexerLocation string = videoIndexerAccount.location

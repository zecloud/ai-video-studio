targetScope = 'resourceGroup'

@description('Azure region shared by Container Apps, Storage, and Durable Task Scheduler.')
param location string = resourceGroup().location
param containerAppsEnvironmentId string
@description('Azure Container Registry name. Override this value when the default is already in use globally.')
param containerRegistryName string = 'acrvideostudio'
@description('Azure AI Foundry account name created in the target resource group.')
param foundryAccountName string = 'aivideoindexerfoundry'
@description('Azure AI Foundry project name created under the account.')
param foundryProjectName string = 'video-indexer-project'
@description('Azure OpenAI account connected to Azure AI Video Indexer.')
param videoIndexerOpenAIAccountName string = 'aivideoindexeropenai'
@description('Azure AI Video Indexer account name. Override this value when the default is already in use.')
param videoIndexerAccountName string = 'videoindexer-prod'
param videoIndexerRoleDefinitionResourceId string
@description('Model deployment name and model name used by the editing worker.')
param foundryDeploymentName string = 'gpt-5.4'
@description('Azure model version for the GPT-5.4 deployment.')
param foundryModelVersion string = '2026-03-05'
@secure()
param serviceApiKey string
param containerImageRepository string = 'ai-video-indexer-service'
param containerImageTag string = 'latest'
param storageAccountName string = toLower('st${uniqueString(resourceGroup().id, 'azure-video-indexer-service')}')
param logAnalyticsWorkspaceName string = toLower('law-${uniqueString(resourceGroup().id, 'azure-video-indexer-service')}')
param appInsightsName string = toLower('appi-${uniqueString(resourceGroup().id, 'azure-video-indexer-service')}')
@description('Durable Task Scheduler resource name. Must be unique only within this resource group.')
param durableTaskSchedulerName string = 'dts-${uniqueString(resourceGroup().id)}'
@description('Durable Task task hub name.')
param durableTaskHubName string = 'video-indexer'
param apiMaxReplicas int = 5
param workerMaxReplicas int = 10

var serviceName = 'azure-video-indexer-service'
var apiAppName = 'video-indexer-api'
var workerAppName = 'video-indexer-worker'
var stagingContainerName = 'video-indexer-staging'
var jobsContainerName = 'video-indexer-jobs'
var acrPullRoleDefinitionId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'
var storageBlobDataContributorRoleDefinitionId = 'ba92f5b4-2d11-453d-a403-e96b0029c9fe'
var cognitiveServicesOpenAIUserRoleDefinitionId = '5e0bd9bd-7b93-4f28-af87-19fc36ad61bd'
var foundryUserRoleDefinitionId = '53ca6127-db72-4b80-b1b0-d745d6d5456d'
var durableTaskDataContributorRoleDefinitionId = '0ad04412-c4d5-4796-b79c-f76d14c8d402'

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' = {
  name: containerRegistryName
  location: location
  sku: { name: 'Basic' }
  properties: {
    adminUserEnabled: false
    publicNetworkAccess: 'Enabled'
  }
}

resource foundryAccount 'Microsoft.CognitiveServices/accounts@2025-06-01' = {
  name: foundryAccountName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  sku: {
    name: 'S0'
  }
  kind: 'AIServices'
  properties: {
    allowProjectManagement: true
    customSubDomainName: foundryAccountName
    disableLocalAuth: false
    publicNetworkAccess: 'Enabled'
  }
}

resource foundryProject 'Microsoft.CognitiveServices/accounts/projects@2025-06-01' = {
  parent: foundryAccount
  name: foundryProjectName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  properties: {}
}

resource foundryModelDeployment 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: foundryAccount
  name: foundryDeploymentName
  sku: {
    name: 'GlobalStandard'
    capacity: 1
  }
  properties: {
    model: {
      name: foundryDeploymentName
      version: foundryModelVersion
      format: 'OpenAI'
    }
  }
}

resource videoIndexerOpenAIAccount 'Microsoft.CognitiveServices/accounts@2025-06-01' = {
  name: videoIndexerOpenAIAccountName
  location: location
  sku: {
    name: 'S0'
  }
  kind: 'OpenAI'
  properties: {
    customSubDomainName: videoIndexerOpenAIAccountName
    disableLocalAuth: false
    publicNetworkAccess: 'Enabled'
  }
}

var foundryProjectEndpoint = 'https://${foundryAccountName}.services.ai.azure.com/api/projects/${foundryProjectName}'

resource videoIndexerAccount 'Microsoft.VideoIndexer/accounts@2025-04-01' = {
  name: videoIndexerAccountName
  location: location
  identity: { type: 'SystemAssigned' }
  properties: {
    storageServices: {
      resourceId: storageAccount.id
      userAssignedIdentity: ''
    }
    openAiServices: {
      resourceId: videoIndexerOpenAIAccount.id
      userAssignedIdentity: ''
    }
  }
}

resource videoIndexerStorageRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(storageAccount.id, videoIndexerAccount.id, storageBlobDataContributorRoleDefinitionId)
  scope: storageAccount
  properties: {
    principalId: videoIndexerAccount.identity.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', storageBlobDataContributorRoleDefinitionId)
  }
}

resource logAnalytics 'Microsoft.OperationalInsights/workspaces@2023-09-01' = {
  name: logAnalyticsWorkspaceName
  location: location
  properties: {
    sku: { name: 'PerGB2018' }
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
  sku: { name: 'Standard_LRS' }
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
  properties: { publicAccess: 'None' }
}

resource jobsContainer 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: storageBlobService
  name: jobsContainerName
  properties: { publicAccess: 'None' }
}

resource scheduler 'Microsoft.DurableTask/schedulers@2025-11-01' = {
  name: durableTaskSchedulerName
  location: location
  properties: {
    sku: { name: 'Consumption' }
    // DTS rejects all gRPC calls when this list is empty. ACA has no stable egress IP.
    ipAllowlist: ['0.0.0.0/0']
  }
}

resource taskHub 'Microsoft.DurableTask/schedulers/taskHubs@2025-11-01' = {
  parent: scheduler
  name: durableTaskHubName
}

resource apiIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: '${apiAppName}-identity'
  location: location
}

resource workerIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: '${workerAppName}-identity'
  location: location
}

var image = '${acr.properties.loginServer}/${containerImageRepository}:${containerImageTag}'
var storageEnvironment = [
  {
    name: 'APPLICATIONINSIGHTS_CONNECTION_STRING'
    secretRef: 'appinsights-connection-string'
  }
  {
    name: 'AZURE_STORAGE_URL'
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
    name: 'DTS_ENDPOINT'
    value: scheduler.properties.endpoint
  }
  {
    name: 'DTS_TASK_HUB'
    value: taskHub.name
  }
  // The pinned Go backend uses DefaultAzureCredential; AZURE_CLIENT_ID selects this app's UAMI.
]

var workerEnvironment = concat(storageEnvironment, [
  {
    name: 'AZURE_VIDEO_INDEXER_SUBSCRIPTION_ID'
    value: subscription().subscriptionId
  }
  {
    name: 'AZURE_VIDEO_INDEXER_RESOURCE_GROUP'
    value: resourceGroup().name
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
    name: 'AZURE_VIDEO_INDEXER_LOCATION'
    value: videoIndexerAccount.location
  }
  {
    name: 'FOUNDRY_PROJECT_ENDPOINT'
    value: foundryProjectEndpoint
  }
  {
    name: 'FOUNDRY_DEPLOYMENT_NAME'
    value: foundryDeploymentName
  }
])
resource apiApp 'Microsoft.App/containerApps@2024-03-01' = {
  name: apiAppName
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${apiIdentity.id}': {}
    }
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
          identity: apiIdentity.id
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
      containers: [{
        name: 'api'
        image: image
        env: concat(storageEnvironment, [
          {
            name: 'AZURE_CLIENT_ID'
            value: apiIdentity.properties.clientId
          }
          {
            name: 'API_KEY'
            secretRef: 'service-api-key'
          }
          {
            name: 'SERVICE_ROLE'
            value: 'api'
          }
          {
            name: 'LISTEN_ADDR'
            value: ':8080'
          }
          {
            name: 'OTEL_SERVICE_NAME'
            value: apiAppName
          }
        ])
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
      }]
      scale: {
        minReplicas: 0
        maxReplicas: apiMaxReplicas
        rules: [
          {
            name: 'http'
            http: {
              metadata: {
                concurrentRequests: '10'
              }
            }
          }
        ]
      }
    }
  }
}

resource workerApp 'Microsoft.App/containerApps@2025-01-01' = {
  name: workerAppName
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${workerIdentity.id}': {}
    }
  }
  properties: {
    environmentId: containerAppsEnvironmentId
    configuration: {
      activeRevisionsMode: 'Single'
      registries: [
        {
          server: acr.properties.loginServer
          identity: workerIdentity.id
        }
      ]
      secrets: [
        {
          name: 'appinsights-connection-string'
          value: appInsights.properties.ConnectionString
        }
      ]
    }
    template: {
      containers: [{
        name: 'worker'
        image: image
        env: concat(workerEnvironment, [
          {
            name: 'AZURE_CLIENT_ID'
            value: workerIdentity.properties.clientId
          }
          {
            name: 'SERVICE_ROLE'
            value: 'worker'
          }
          {
            name: 'OTEL_SERVICE_NAME'
            value: workerAppName
          }
        ])
        resources: {
          cpu: json('0.5')
          memory: '1Gi'
        }
      }]
      scale: {
        minReplicas: 0
        maxReplicas: workerMaxReplicas
        rules: [
          {
            name: 'dts-orchestration'
            custom: {
              type: 'azure-durabletask-scheduler'
              metadata: {
                endpoint: scheduler.properties.endpoint
                maxConcurrentWorkItemsCount: '1'
                taskhubName: taskHub.name
                workItemType: 'Orchestration'
              }
              identity: workerIdentity.id
            }
          }
          {
            name: 'dts-activity'
            custom: {
              type: 'azure-durabletask-scheduler'
              metadata: {
                endpoint: scheduler.properties.endpoint
                maxConcurrentWorkItemsCount: '5'
                taskhubName: taskHub.name
                workItemType: 'Activity'
              }
              identity: workerIdentity.id
            }
          }
        ]
      }
    }
  }
}

resource apiStorageRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(storageAccount.id, apiIdentity.id, storageBlobDataContributorRoleDefinitionId)
  scope: storageAccount
  properties: {
    principalId: apiIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', storageBlobDataContributorRoleDefinitionId)
  }
}

resource workerStorageRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(storageAccount.id, workerIdentity.id, storageBlobDataContributorRoleDefinitionId)
  scope: storageAccount
  properties: {
    principalId: workerIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', storageBlobDataContributorRoleDefinitionId)
  }
}

resource apiDtsRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(scheduler.id, apiIdentity.id, durableTaskDataContributorRoleDefinitionId)
  scope: scheduler
  properties: {
    principalId: apiIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', durableTaskDataContributorRoleDefinitionId)
  }
}

resource workerDtsRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(scheduler.id, workerIdentity.id, durableTaskDataContributorRoleDefinitionId)
  scope: scheduler
  properties: {
    principalId: workerIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', durableTaskDataContributorRoleDefinitionId)
  }
}

module apiAcrPull 'acr-role-assignment.bicep' = {
  name: 'api-acr-pull'
  dependsOn: [acr]
  params: {
    registryName: containerRegistryName
    principalId: apiIdentity.properties.principalId
    roleDefinitionId: acrPullRoleDefinitionId
    assignmentSeed: apiAppName
  }
}

module workerAcrPull 'acr-role-assignment.bicep' = {
  name: 'worker-acr-pull'
  dependsOn: [acr]
  params: {
    registryName: containerRegistryName
    principalId: workerIdentity.properties.principalId
    roleDefinitionId: acrPullRoleDefinitionId
    assignmentSeed: workerAppName
  }
}

module workerFoundryRole 'foundry-role-assignment.bicep' = {
  name: 'worker-foundry-user'
  params: {
    accountName: foundryAccountName
    principalId: workerIdentity.properties.principalId
    roleDefinitionId: foundryUserRoleDefinitionId
    assignmentSeed: workerAppName
  }
}

module videoIndexerFoundryRole 'foundry-role-assignment.bicep' = {
  name: 'video-indexer-openai-user'
  params: {
    accountName: videoIndexerOpenAIAccountName
    principalId: videoIndexerAccount.identity.principalId
    roleDefinitionId: cognitiveServicesOpenAIUserRoleDefinitionId
    assignmentSeed: videoIndexerAccountName
  }
}

module workerVideoIndexerRole 'video-indexer-role-assignment.bicep' = {
  name: 'worker-video-indexer'
  scope: resourceGroup()
  dependsOn: [
    videoIndexerAccount
  ]
  params: {
    accountName: videoIndexerAccountName
    principalId: workerIdentity.properties.principalId
    roleDefinitionResourceId: videoIndexerRoleDefinitionResourceId
    assignmentSeed: workerAppName
  }
}

output containerAppName string = apiApp.name
output containerAppFqdn string = apiApp.properties.configuration.ingress.fqdn
output workerContainerAppName string = workerApp.name
output durableTaskSchedulerEndpoint string = scheduler.properties.endpoint
output durableTaskHubName string = taskHub.name
output storageAccountResourceId string = storageAccount.id
output storageAccountName string = storageAccount.name
output storageAccountBlobEndpoint string = storageAccount.properties.primaryEndpoints.blob
output storageStagingContainerName string = stagingContainer.name
output storageJobsContainerName string = jobsContainer.name
output appInsightsResourceId string = appInsights.id
output logAnalyticsWorkspaceResourceId string = logAnalytics.id
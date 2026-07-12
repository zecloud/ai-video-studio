targetScope = 'resourceGroup'

param location string = resourceGroup().location
@description('Azure Container Registry name. Override this value when the default is already in use globally.')
param containerRegistryName string = 'acrvideostudio'

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' = {
  name: containerRegistryName
  location: location
  sku: { name: 'Basic' }
  properties: {
    adminUserEnabled: false
    publicNetworkAccess: 'Enabled'
  }
}

output resourceId string = acr.id
output loginServer string = acr.properties.loginServer

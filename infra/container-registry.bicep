targetScope = 'resourceGroup'

param location string = resourceGroup().location
param containerRegistryName string

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

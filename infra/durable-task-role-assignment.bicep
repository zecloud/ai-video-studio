targetScope = 'resourceGroup'

param schedulerName string
param principalId string
param roleDefinitionId string

resource scheduler 'Microsoft.DurableTask/schedulers@2025-11-01' existing = {
  name: schedulerName
}

resource roleAssignment 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(scheduler.id, principalId, roleDefinitionId)
  scope: scheduler
  properties: {
    principalId: principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', roleDefinitionId)
  }
}

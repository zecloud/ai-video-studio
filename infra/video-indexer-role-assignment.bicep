targetScope = 'resourceGroup'

param accountName string
param principalId string
param roleDefinitionResourceId string

resource account 'Microsoft.VideoIndexer/accounts@2024-01-01' existing = {
  name: accountName
}

resource roleAssignment 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(account.id, principalId, roleDefinitionResourceId)
  scope: account
  properties: {
    principalId: principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleDefinitionResourceId
  }
}

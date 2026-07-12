targetScope = 'resourceGroup'

param accountName string
param accountResourceGroupName string
param accountSubscriptionId string
param principalId string
param roleDefinitionId string
param assignmentSeed string

resource account 'Microsoft.CognitiveServices/accounts@2023-05-01' existing = {
  name: accountName
  scope: resourceGroup(accountSubscriptionId, accountResourceGroupName)
}

resource roleAssignment 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(account.id, assignmentSeed, roleDefinitionId)
  scope: account
  properties: {
    principalId: principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', roleDefinitionId)
  }
}

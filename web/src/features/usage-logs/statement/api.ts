/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { t } from 'i18next'

import type { ApiResponse } from '@/features/profile/types'
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type { StatementData, StatementParams } from './types'

function buildStatementParams(
  params: StatementParams,
  isAdmin: boolean
): Record<string, string | number> {
  const query: Record<string, string | number> = { month: params.month }
  if (isAdmin && params.user_id) {
    query.user_id = params.user_id
  }
  return query
}

export async function getStatement(
  params: StatementParams,
  isAdmin: boolean
): Promise<StatementData> {
  const response = await api.get<ApiResponse<StatementData>>(
    isAdmin ? '/api/log/statement' : '/api/log/self/statement',
    { params: buildStatementParams(params, isAdmin) }
  )
  if (!response.data.success || !response.data.data) {
    throw createServerError(response.data, t('Failed to load statement'))
  }
  return response.data.data
}

export async function downloadStatementCsv(
  params: StatementParams,
  isAdmin: boolean
): Promise<void> {
  const response = await api.get<Blob>(
    isAdmin ? '/api/log/statement/export' : '/api/log/self/statement/export',
    { params: buildStatementParams(params, isAdmin), responseType: 'blob' }
  )
  const url = URL.createObjectURL(response.data)
  const link = document.createElement('a')
  link.href = url
  link.download = `statement-${params.month}.csv`
  document.body.append(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

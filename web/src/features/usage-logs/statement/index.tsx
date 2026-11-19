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
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Download } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { TitledCard } from '@/components/ui/titled-card'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { downloadStatementCsv, getStatement } from './api'
import type { StatementGroup } from './types'

function currentMonth(): string {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
}

function shiftMonth(month: string, delta: number): string {
  const [year, mon] = month.split('-').map(Number)
  if (!year || !mon) return month
  const date = new Date(year, mon - 1 + delta, 1)
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}`
}

export function Statement() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const user = useAuthStore((state) => state.auth.user)
  const isAdmin = !!user && user.role >= ROLE.ADMIN

  const [month, setMonth] = useState(currentMonth)
  const [userIdInput, setUserIdInput] = useState('')
  const [userId, setUserId] = useState<number | undefined>(undefined)
  const [exporting, setExporting] = useState(false)

  const query = useQuery({
    queryKey: ['statement', isAdmin, month, userId ?? 0],
    queryFn: () => getStatement({ month, user_id: userId }, isAdmin),
  })

  const applyUserId = () => {
    const parsed = Number.parseInt(userIdInput, 10)
    setUserId(Number.isInteger(parsed) && parsed > 0 ? parsed : undefined)
  }

  const handleExport = async () => {
    setExporting(true)
    try {
      await downloadStatementCsv({ month, user_id: userId }, isAdmin)
    } catch (error) {
      handleServerError(error, t('Failed to export statement'))
    } finally {
      setExporting(false)
    }
  }

  const usageColumns: StaticDataTableColumn<StatementGroup>[] = [
    {
      id: 'requests',
      header: t('Requests'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatNumber(row.requests, locale),
    },
    {
      id: 'prompt_tokens',
      header: t('Input Tokens'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatNumber(row.prompt_tokens, locale),
    },
    {
      id: 'completion_tokens',
      header: t('Output Tokens'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatNumber(row.completion_tokens, locale),
    },
    {
      id: 'token_used',
      header: t('Total Tokens'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatNumber(row.token_used, locale),
    },
    {
      id: 'quota',
      header: t('Cost'),
      className: 'text-right',
      cellClassName: 'text-right tabular-nums',
      cell: (row) => formatQuotaWithCurrency(row.quota),
    },
  ]

  const byModelColumns: StaticDataTableColumn<StatementGroup>[] = [
    {
      id: 'model_name',
      header: t('Model'),
      cell: (row) => row.model_name || '-',
    },
    ...usageColumns,
  ]

  const byChannelColumns: StaticDataTableColumn<StatementGroup>[] = [
    {
      id: 'channel',
      header: t('Channel'),
      cell: (row) =>
        row.channel_name
          ? `#${row.channel_id} ${row.channel_name}`
          : `#${row.channel_id}`,
    },
    ...usageColumns,
  ]

  const data = query.data
  const isEmpty = !!data && data.total.requests === 0

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Statement')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          type='button'
          variant='outline'
          onClick={handleExport}
          disabled={exporting || query.isLoading}
        >
          <Download aria-hidden='true' />
          {t('Export CSV')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='flex flex-col gap-4'>
          <div className='flex flex-wrap items-end gap-3'>
            <div className='flex flex-col gap-1.5'>
              <span className='text-sm font-medium'>{t('Month')}</span>
              <div className='flex items-center gap-1'>
                <Button
                  type='button'
                  variant='outline'
                  size='icon'
                  aria-label={t('Previous month')}
                  onClick={() => setMonth((m) => shiftMonth(m, -1))}
                >
                  <ChevronLeft aria-hidden='true' />
                </Button>
                <Input
                  type='month'
                  aria-label={t('Month')}
                  className='w-44'
                  value={month}
                  onChange={(event) => {
                    if (event.target.value) setMonth(event.target.value)
                  }}
                />
                <Button
                  type='button'
                  variant='outline'
                  size='icon'
                  aria-label={t('Next month')}
                  onClick={() => setMonth((m) => shiftMonth(m, 1))}
                >
                  <ChevronRight aria-hidden='true' />
                </Button>
              </div>
            </div>
            {isAdmin && (
              <div className='flex flex-col gap-1.5'>
                <label
                  htmlFor='statement-user-id'
                  className='text-sm font-medium'
                >
                  {t('User ID')}
                </label>
                <Input
                  id='statement-user-id'
                  type='number'
                  min={1}
                  className='w-44'
                  placeholder={t('All users')}
                  value={userIdInput}
                  onChange={(event) => setUserIdInput(event.target.value)}
                  onBlur={applyUserId}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') applyUserId()
                  }}
                />
              </div>
            )}
          </div>

          {query.isLoading && <LoadingState />}
          {query.isError && (
            <ErrorState
              description={t('Failed to load statement')}
              onRetry={() => void query.refetch()}
            />
          )}
          {data && isEmpty && (
            <EmptyState
              title={t('No data')}
              description={t('No usage recorded for this month.')}
            />
          )}
          {data && !isEmpty && (
            <>
              <TitledCard title={t('Total')}>
                <dl className='grid grid-cols-2 gap-4 sm:grid-cols-5'>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Requests')}
                    </dt>
                    <dd className='text-lg font-semibold tabular-nums'>
                      {formatNumber(data.total.requests, locale)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Input Tokens')}
                    </dt>
                    <dd className='text-lg font-semibold tabular-nums'>
                      {formatNumber(data.total.prompt_tokens, locale)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Output Tokens')}
                    </dt>
                    <dd className='text-lg font-semibold tabular-nums'>
                      {formatNumber(data.total.completion_tokens, locale)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Total Tokens')}
                    </dt>
                    <dd className='text-lg font-semibold tabular-nums'>
                      {formatNumber(data.total.token_used, locale)}
                    </dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground text-xs'>
                      {t('Cost')}
                    </dt>
                    <dd className='text-lg font-semibold tabular-nums'>
                      {formatQuotaWithCurrency(data.total.quota)}
                    </dd>
                  </div>
                </dl>
              </TitledCard>

              <TitledCard title={t('By Model')}>
                <StaticDataTable
                  data={data.by_model}
                  columns={byModelColumns}
                  getRowKey={(row) => row.model_name ?? ''}
                  emptyContent={t('No data')}
                />
              </TitledCard>

              <TitledCard title={t('By Channel')}>
                <StaticDataTable
                  data={data.by_channel}
                  columns={byChannelColumns}
                  getRowKey={(row) => String(row.channel_id ?? '')}
                  emptyContent={t('No data')}
                />
              </TitledCard>
            </>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

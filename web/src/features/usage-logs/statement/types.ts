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
/** One aggregated line of a statement: usage for one model or one channel. */
export interface StatementGroup {
  model_name?: string
  channel_id?: number
  channel_name?: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  token_used: number
  quota: number
}

export interface StatementData {
  month: string
  start_timestamp: number
  end_timestamp: number
  user_id: number
  by_model: StatementGroup[]
  by_channel: StatementGroup[]
  total: StatementGroup
}

export interface StatementParams {
  /** Month in `YYYY-MM` form. */
  month: string
  /** Administrators only: scope the statement to one user. */
  user_id?: number
}

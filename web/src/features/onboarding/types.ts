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
export interface OnboardingSlot {
  id: string
  name: string
  available: boolean
  reason?: string
  logged_in?: boolean
  assigned?: boolean
}

export interface OnboardingInstance {
  id: string
  name: string
  available: boolean
  error?: string
  slots: OnboardingSlot[]
  exits?: { id: string; name: string; available: boolean; reason?: string }[]
}

export interface OnboardingOptions {
  instances: OnboardingInstance[]
  models: string[]
  api_base_url?: string
}

export interface OnboardingRequest {
  request_id: string
  username: string
  display_name: string
  password: string
  quota: number
  instance_id: string
  slot_id: string
}

export interface OnboardingResult {
  request_id: string
  status: 'completed' | 'pending' | 'failed'
  user_id?: number
  username: string
  group?: string
  instance_id: string
  slot_id: string
  channel_id?: number
  message?: string
  display_name?: string
  quota?: number
}

export interface CreateSlotRequest {
  request_id: string
  instance_id: string
  name: string
  exit_id: string
}

export interface SlotLoginSession {
  auth_url: string
  session_id: string
  expires_at: string | number
}

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
import { Link } from '@tanstack/react-router'
import { ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { useIsSidebarModuleVisible } from '@/hooks/use-sidebar-config'
import { useStatus } from '@/hooks/use-status'

import { LanguagePreferencesCard } from './components/language-preferences-card'
import { ProfileHeader } from './components/profile-header'
import { ProfileSettingsCard } from './components/profile-settings-card'
import { useProfile } from './hooks'

/** 个人设置仅呈现账号与语言；安全操作沿用原来的受保护页面及校验。 */
export function Profile() {
  const { t } = useTranslation()
  const { profile, loading, refreshProfile } = useProfile()
  const { status } = useStatus()
  const showSecurity = useIsSidebarModuleVisible('/security')
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Personal settings')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        {showSecurity && (
          <Button variant='outline' size='sm' render={<Link to='/security' />}>
            <ShieldCheck />
            {t('Security & Access')}
          </Button>
        )}
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='mx-auto w-full max-w-5xl space-y-5 pb-6'>
          <ProfileHeader profile={profile} loading={loading} />
          <ProfileSettingsCard
            profile={profile}
            loading={loading}
            onProfileUpdate={refreshProfile}
            quotaRemindEnabled={status?.quota_remind_enabled !== false}
            quotaRemindThreshold={status?.quota_remind_threshold ?? 0}
          />
          <LanguagePreferencesCard
            profile={profile}
            onProfileUpdate={refreshProfile}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

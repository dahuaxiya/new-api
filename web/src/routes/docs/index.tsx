import { createFileRoute } from '@tanstack/react-router'

import { UserDocs } from '@/features/user-docs'

export const Route = createFileRoute('/docs/')({ component: UserDocs })

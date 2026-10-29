import {changeDashboard,type DashboardState,type DashboardChange} from '../../client/services/dashboard-subscriptions';
export {emptyDashboard,serviceDirectory,dashboardChangedEvent} from '../../client/services/dashboard-subscriptions';
let state:DashboardState={revision:1,subscriptions:['prod/payment','prod/identity','prod/checkout'].map(service=>({service,created:1790553600000})),service:'',drawerOpen:false};
export async function dashboardPartition(){return 'snoot-static-preview'}
export async function loadDashboard(){return structuredClone(state)}
export async function saveDashboardChange(_key:string,change:DashboardChange){state=changeDashboard(state,change,1793145600000);return structuredClone(state)}

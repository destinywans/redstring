import groovy.json.JsonSlurper
import hudson.model.FreeStyleProject
import hudson.model.Item
import hudson.security.Permission
import hudson.security.ACL
import hudson.security.AuthorizationStrategy
import hudson.security.HudsonPrivateSecurityRealm
import hudson.tasks.Shell
import jenkins.model.Jenkins
import jenkins.security.ApiTokenProperty
import org.springframework.security.core.Authentication

// Disposable single-job lab only. The unchanged original Go live gate has no
// authentication options, so anonymous gets READ, never Build or configuration.
class TestlabReadOnlyStrategy extends AuthorizationStrategy {
    ACL getRootACL() {
        return new ACL() {
            boolean hasPermission2(Authentication authentication, Permission permission) {
                String name = authentication.name
                if (name == 'testlab-admin' || name == 'SYSTEM') return true
                if (name != 'testlab-reader' && name != 'anonymous') return false
                return permission == Jenkins.READ || permission == Item.READ || permission == Item.DISCOVER
            }
        }
    }
    Collection<String> getGroups() { return Collections.emptyList() }
}

def cfg = new JsonSlurper().parse(new File('/testlab-config.json'))
def instance = Jenkins.get()
if (!instance.items.empty) throw new IllegalStateException('Fresh single-job Jenkins required')
instance.setNumExecutors(1)
instance.setSlaveAgentPort(-1)
instance.setNoUsageStatistics(true)
instance.updateCenter.sites.clear()
instance.updateCenter.save()
def realm = new HudsonPrivateSecurityRealm(false)
realm.createAccount('testlab-admin', cfg.admin_password)
def reader = realm.createAccount('testlab-reader', cfg.reader_password)
instance.setSecurityRealm(realm)
instance.setAuthorizationStrategy(new TestlabReadOnlyStrategy())
def token = reader.getProperty(ApiTokenProperty.class).tokenStore.generateNewToken('owned-readcheck')
reader.save()
def tokenFile = new File(instance.rootDir, 'testlab-reader-token')
tokenFile.text = token.plainValue
tokenFile.setReadable(false, false)
tokenFile.setWritable(false, false)
tokenFile.setReadable(true, true)
tokenFile.setWritable(true, true)
def job = instance.createProject(FreeStyleProject.class, cfg.job as String)
job.buildersList.add(new Shell("printf '%s\\n' 'testlab-controlled-build-${cfg.run_id}'"))
job.save()
instance.save()
if (job.scheduleBuild2(0) == null) throw new IllegalStateException('Controlled build was not queued')

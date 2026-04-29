package com.example.emojichat

import com.zaxxer.hikari.HikariConfig
import com.zaxxer.hikari.HikariDataSource
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.autoconfigure.SpringBootApplication
import org.springframework.boot.autoconfigure.jdbc.DataSourceAutoConfiguration
import org.springframework.boot.runApplication
import org.springframework.boot.web.servlet.FilterRegistrationBean
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration
import org.springframework.core.Ordered
import org.springframework.web.cors.CorsConfiguration
import org.springframework.web.cors.UrlBasedCorsConfigurationSource
import org.springframework.web.filter.CorsFilter
import javax.sql.DataSource

@SpringBootApplication(exclude = [DataSourceAutoConfiguration::class])
class EmojiChatApplication

fun main(args: Array<String>) {
    runApplication<EmojiChatApplication>(*args)
}

@Configuration
class DataSourceConfig {
    private val log = LoggerFactory.getLogger(DataSourceConfig::class.java)

    @Bean
    fun dataSource(
        @Value("\${emojichat.db-type:sqlite}") dbType: String,
        @Value("\${emojichat.mysql-dsn}") mysqlDsn: String,
        @Value("\${emojichat.mysql-user}") mysqlUser: String,
        @Value("\${emojichat.mysql-password}") mysqlPassword: String,
        @Value("\${SQLITE_PATH:emoji_chat.db}") sqlitePath: String,
    ): DataSource {
        val cfg = HikariConfig()
        when (dbType.lowercase()) {
            "mysql" -> {
                cfg.jdbcUrl = mysqlDsn
                cfg.username = mysqlUser
                cfg.password = mysqlPassword
                cfg.driverClassName = "com.mysql.cj.jdbc.Driver"
                cfg.maximumPoolSize = 25
                cfg.minimumIdle = 5
                log.info("Connecting to MySQL")
            }
            "sqlite" -> {
                cfg.jdbcUrl = "jdbc:sqlite:$sqlitePath"
                cfg.driverClassName = "org.sqlite.JDBC"
                cfg.maximumPoolSize = 1
                log.info("Connecting to SQLite ({})", sqlitePath)
            }
            else -> error("Unsupported emojichat.db-type: $dbType (use 'mysql' or 'sqlite')")
        }
        return HikariDataSource(cfg)
    }

    @Bean
    fun corsFilter(): FilterRegistrationBean<CorsFilter> {
        val source = UrlBasedCorsConfigurationSource()
        val cfg = CorsConfiguration().apply {
            addAllowedOriginPattern("*")
            addAllowedHeader("*")
            addAllowedMethod("*")
            allowCredentials = false
        }
        source.registerCorsConfiguration("/**", cfg)
        return FilterRegistrationBean(CorsFilter(source)).apply {
            order = Ordered.HIGHEST_PRECEDENCE
        }
    }
}
